#!/usr/bin/env bash
# 术语门禁(ADR-010)
#
# 账号内核(internal/identity)与平台层(in/platform、internal/platform)必须保持域中立,
# 不得出现 oidc / minecraft / skin / mc 等业务词,防止内核长出业务耦合。
#
# 编译期挡不住「在核心里写 if domain == "mc"」,所以用 grep 做最后一道关。
#
# 豁免:identity.account.mc_login_enabled 是数据库列名,内核只当通用布尔开关处理,
# 不理解其语义(见 docs/03-data-model.md 第一节说明)。这一项显式豁免并记录在案。

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TARGET_DIRS=(internal/identity internal/platform internal/domain)

# 命中的业务词。\b 保证只匹配独立单词,不会误伤 "mock" / "schema" 之类的词。
PATTERN='\b(oidc|oauth|minecraft|skin|cloak|yggdrasil|mc_profile|mc_login|mc_token)\b'

# 显式豁免项,每一项都必须写明理由。
ALLOWLIST=(
  # account.mc_login_enabled:数据库列名,内核视作通用布尔开关
  '\bmc_login_enabled\b'
  '\bMCLoginEnabled\b'
)

FAIL=0
HITS=()

for dir in "${TARGET_DIRS[@]}"; do
  [ -d "$dir" ] || continue
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    allowed=0
    for a in "${ALLOWLIST[@]}"; do
      if grep -qE "$a" <<<"$line"; then
        allowed=1
        break
      fi
    done
    [ "$allowed" -eq 0 ] && HITS+=("$line")
  done < <(grep -rnE "$PATTERN" "$dir" --include='*.go' 2>/dev/null)
done

if [ ${#HITS[@]} -ne 0 ]; then
  echo "术语门禁失败:内核/平台层出现业务词(ADR-010)"
  printf '  %s\n' "${HITS[@]}"
  echo
  echo "如果确实需要,请改用域中立命名,或在上方 ALLOWLIST 中显式豁免并写明理由。"
  FAIL=1
else
  echo "术语门禁通过:内核与平台层保持域中立。"
fi

exit "$FAIL"