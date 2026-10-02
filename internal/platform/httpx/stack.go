package httpx

import "runtime"

// stackTrace 取当前调用栈。单独抽出成变量,便于测试注入假堆栈,
// 避免断言依赖真实的运行时栈内容。
var stackTrace = func() string {
	buf := make([]byte, 8<<10)
	return string(buf[:runtime.Stack(buf, false)])
}
