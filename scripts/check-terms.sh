#!/usr/bin/env bash
# 术语门禁(ADR-010)
#
# 账号内核(internal/identity)、平台层(internal/platform)与共享值对象(internal/domain)
# 必须保持域中立,不得出现 oidc / minecraft / skin / mc 等业务词,
# 防止内核长出业务耦合。
#
# 编译期挡不住「在核心里写 if domain == "mc"」,所以用 grep 做最后一道关。
#
# 不检查的范围:
#   - sqlc 生成物(internal/platform/db/query/):由 db/queries 派生,而那里天然含业务语义
#   - 测试文件:测试需要能直接引用真实业务名词
#
# 豁免:identity.account.mc_login_enabled 是数据库列名,内核只当通用布尔开关处理,
# 不理解其语义(见 docs/03-data-model.md 第一节说明)。这一项显式豁免并记录在案。

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TARGET_DIRS=(internal/identity internal/platform internal/domain)

# 命中的业务词。\b 保证只匹配独立单词,不会误伤 "mock" / "schema" 之类的词。
PATTERN='\b(oidc|oauth|minecraft|skin|cloak|yggdrasil|mc)\b'

# 显式豁免项,每一项都必须写明理由。
ALLOWLIST=(
  # account.mc_login_enabled:数据库列名,内核视作通用布尔开关,不承载业务语义
  '\bmc_login_enabled\b'
  '\bMCLoginEnabled\b'
  '\bMcLoginEnabled\b'
  '\bSetMCLoginEnabled\b'
  # 指向设计文档的链接不构成业务耦合 ——
  # 恰恰相反,把「为什么这么做」写清楚是「文档即契约」的一部分
  'docs/0[0-9]-[a-z-]+\.md'
)

# 整文件豁免:<文件相对路径>|<理由>
FILE_ALLOWLIST=(
  # 弱口令表:收录的是**用户可能真的设置的字符串**,
  # 不是内核的业务语义;拒绝它们正是密码策略的职责
  'internal/identity/account/password.go|弱口令表收录的是用户可能真的设置的字符串,不是业务语义'
  # 测试脚手架:集成测试必须能按真实 schema 名建表与清理
  'internal/platform/db/testdb/testdb.go|测试脚手架需要按真实 schema 名建表与清理'
)

FAIL=0
HITS=()

for dir in "${TARGET_DIRS[@]}"; do
  [ -d "$dir" ] || continue
  while IFS= read -r line; do
    [ -z "$line" ] && continue

    file="${line%%:*}"
    allowed=0

    for entry in "${FILE_ALLOWLIST[@]}"; do
      if [[ "$file" == "${entry%%|*}" ]]; then
        allowed=1
        break
      fi
    done
    [ "$allowed" -eq 1 ] && continue

    for a in "${ALLOWLIST[@]}"; do
      if grep -qE "$a" <<<"$line"; then
        allowed=1
        break
      fi
    done
    [ "$allowed" -eq 0 ] && HITS+=("$line")
  done < <(grep -rnE "$PATTERN" "$dir" \
    --include='*.go' \
    --exclude-dir=query \
    --exclude='*_test.go' 2>/dev/null)
done

if [ ${#HITS[@]} -ne 0 ]; then
  echo "术语门禁失败:内核/平台层出现业务词(ADR-010)"
  printf '  %s\n' "${HITS[@]}"
  echo
  echo "如果确实需要,请改用域中立命名,或在上方 ALLOWLIST / FILE_ALLOWLIST 中显式豁免并写明理由。"
  FAIL=1
else
  echo "术语门禁通过:内核、平台层与共享值对象保持域中立。"
fi

exit "$FAIL"