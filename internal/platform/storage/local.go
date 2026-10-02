package storage

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// Local 是基于本地磁盘的存储实现。
//
// 选它而不是内存有两个理由:重启后文件还在(内存实现会让每次重启
// 丢掉全部纹理),以及它能真实暴露「并发写同一目录」这类问题。
type Local struct {
	root     string
	dirMode  fs.FileMode
	fileMode fs.FileMode
}

// LocalOptions 是 Local 的构造参数。
type LocalOptions struct {
	// Root 是存储根目录。相对路径按进程工作目录解析。
	Root string
	// DirMode 是目录权限,默认 0755。
	DirMode fs.FileMode
	// FileMode 是文件权限,默认 0644。
	//
	// 0644 而不是 0600 是刻意的:纹理由 nginx 之类的静态服务器
	// 直接读取,进程本身不参与下载路径。收紧到 0600 会让运维
	// 不得不给反向代理开同组权限,最后往往是直接改成 0777。
	FileMode fs.FileMode
}

// NewLocal 创建本地存储并确保根目录存在。
func NewLocal(opts LocalOptions) (*Local, error) {
	if opts.Root == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "存储根目录不能为空")
	}
	if opts.DirMode == 0 {
		opts.DirMode = 0o755
	}
	if opts.FileMode == 0 {
		opts.FileMode = 0o644
	}

	abs, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "解析存储根目录失败: %v", err)
	}
	if err := os.MkdirAll(abs, opts.DirMode); err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "创建存储根目录失败: %v", err)
	}

	return &Local{root: abs, dirMode: opts.DirMode, fileMode: opts.FileMode}, nil
}

// 编译期确认实现了接口。
var _ Storage = (*Local)(nil)

// Put 写入对象。
//
// 先写临时文件再 rename:rename 在同一文件系统内是原子的,
// 这样并发读到一半的 PNG 不会暴露给客户端。直接写目标路径的话,
// 一个正在下载的请求会拿到缺了尾部数据的文件。
func (l *Local) Put(_ context.Context, key string, data []byte) error {
	full, err := l.resolve(key)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(full), l.dirMode); err != nil {
		return apperr.Newf(apperr.CodeInternal, "创建存储目录失败: %v", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return apperr.Newf(apperr.CodeInternal, "创建临时文件失败: %v", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return apperr.Newf(apperr.CodeInternal, "写入存储文件失败: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return apperr.Newf(apperr.CodeInternal, "关闭临时文件失败: %v", err)
	}

	if err := os.Rename(tmpName, full); err != nil {
		_ = os.Remove(tmpName)
		return apperr.Newf(apperr.CodeInternal, "提交存储文件失败: %v", err)
	}
	// CreateTemp 默认 0600。显式 chmod 到目标权限,否则 nginx
	// 会读不到 —— 而这是最难排查的一类故障:应用侧一切正常。
	if err := os.Chmod(full, l.fileMode); err != nil {
		return apperr.Newf(apperr.CodeInternal, "设置文件权限失败: %v", err)
	}
	return nil
}

// Get 读取对象。
func (l *Local) Get(_ context.Context, key string) ([]byte, error) {
	full, err := l.resolve(key)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, apperr.Newf(apperr.CodeInternal, "读取存储文件失败: %v", err)
	}
	return data, nil
}

// Delete 删除对象;不存在不算错误。
func (l *Local) Delete(_ context.Context, key string) error {
	full, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return apperr.Newf(apperr.CodeInternal, "删除存储文件失败: %v", err)
	}
	return nil
}

// Exists 判断对象是否存在。
func (l *Local) Exists(_ context.Context, key string) (bool, error) {
	full, err := l.resolve(key)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, apperr.Newf(apperr.CodeInternal, "检查存储文件失败: %v", err)
	}
	return !info.IsDir(), nil
}

// resolve 把键映射成磁盘路径,并挡住路径穿越。
//
// 键来自数据库里的 sha256 或 profile uuid,理论上不含 `..`。
// 但这是一个从外部输入可达的写盘路径 —— 与其相信「理论上」,
// 不如在这里挡一次。代价只有一次字符串检查。
func (l *Local) resolve(key string) (string, error) {
	if key == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "存储键不能为空")
	}
	if strings.Contains(key, "..") || strings.HasPrefix(key, "/") || filepath.IsAbs(key) {
		return "", apperr.Newf(apperr.CodeInvalidArgument, "非法的存储键: %q", key)
	}

	full := filepath.Join(l.root, filepath.Clean(key))
	rel, err := filepath.Rel(l.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", apperr.Newf(apperr.CodeInvalidArgument, "非法的存储键: %q", key)
	}
	return full, nil
}

// Root 返回存储根目录的绝对路径。
func (l *Local) Root() string { return l.root }
