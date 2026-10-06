#!/usr/bin/env bash
# 黄金路径验收剧本 —— docs/configuration.md §8 的可执行版。
#
# 七步里,后端能自动判定的那部分(登录后台、改站点名、发测试信、关注册)
# 在 internal/admin/goldenpath_integration_test.go 里已有对应断言;
# 这个脚本负责**真容器**那部分:起容器、看引导日志、在真环境里把 4/5/6
# 再走一遍、最后验证「改 .env → 重启 → 生效」。
#
# 用法:
#   ADMIN_PASSWORD='<从启动日志抄>' bash deploy/test/golden-path.sh
#
# 环境变量:
#   BASE_URL              已在跑的服务(默认 http://127.0.0.1:3000)
#   ADMIN_EMAIL           引导出的管理员邮箱(默认 admin@localhost.local,
#                         与 .env 的 ADMIN_EMAIL 保持一致)
#   ADMIN_PASSWORD        管理员密码(必填;.env 里 ADMIN_PASSWORD 留空时,
#                         随机密码只在**启动日志**里打印一次,从那里抄)
#   GOLDEN_NEW_PASSWORD   首登强制改密时用的新密码(默认在原密码后加 9)
#   GOLDEN_TO             测试信收件地址(默认 ADMIN_EMAIL)
#   GOLDEN_SITE           新站点名(默认 晨星账号)
#   GOLDEN_SKIP_DOCKER=1  不起容器,只对已在跑的服务跑第 3–6 步
#
# 关于跳过:步骤 1/2/7 需要 docker。本机没有 Docker daemon 时它们会被
# **如实跳过**并打印原因 —— 这条脚本的可信度来自「跳过就说跳过」,
# 绝不用一个假成功糊过去。

set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

BASE_URL="${BASE_URL:-http://127.0.0.1:3000}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@localhost.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
GOLDEN_TO="${GOLDEN_TO:-$ADMIN_EMAIL}"
GOLDEN_SITE="${GOLDEN_SITE:-晨星账号}"
GOLDEN_SKIP_DOCKER="${GOLDEN_SKIP_DOCKER:-0}"

PASS=0
FAIL=0
SKIPPED=0
COOKIE_JAR="$(mktemp)"
ENV_BACKUP=""

step()  { printf '\n\033[1m=== %s\033[0m\n' "$*"; }
info()  { printf '  %s\n' "$*"; }
ok()    { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS + 1)); }
bad()   { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL + 1)); }
skip()  { printf '  \033[33m⊘\033[0m 跳过:%s\n' "$*"; SKIPPED=$((SKIPPED + 1)); }

have() { command -v "$1" >/dev/null 2>&1; }

cleanup() {
  rm -f "$COOKIE_JAR"
  # 第 7 步改过 .env 就一定要还原:验收脚本不该在别人的机器上留痕迹
  if [ -n "$ENV_BACKUP" ] && [ -f "$ENV_BACKUP" ]; then
    mv "$ENV_BACKUP" .env
    info "已还原 .env"
  fi
}
trap cleanup EXIT

# ---------------------------------------------------------------- 小工具

# envelope_code <json>:取 {"code":…}。成功是 0(ADR-008)。
envelope_code() {
  printf '%s' "$1" | grep -o '"code":[0-9-]*' | head -1 | cut -d: -f2
}

# json_field <json> <field>:从 envelope 的 data 里取一个字段。
# 有 jq 就用 jq;没有就用 grep/sed —— 这脚本要在一台什么都没装的机器上跑。
json_field() {
  local json=$1 field=$2
  if have jq; then
    printf '%s' "$json" | jq -r --arg f "$field" '.data[$f] // empty'
  else
    printf '%s' "$json" \
      | grep -o "\"$field\"[[:space:]]*:[^,}]*" | head -1 \
      | sed 's/^[^:]*:[[:space:]]*//; s/^"//; s/"$//'
  fi
}

# api <METHOD> <PATH> [JSON_BODY]:带会话 cookie 与同源头(CSRF 是同源校验)。
api() {
  local method=$1 path=$2 body=${3-}
  local args=(-sS -X "$method" "$BASE_URL$path"
    -H 'Content-Type: application/json' -H "Origin: $BASE_URL"
    -b "$COOKIE_JAR" -c "$COOKIE_JAR" --max-time 30)
  [ -n "$body" ] && args+=(--data "$body")
  curl "${args[@]}" 2>/dev/null
}

expect_code0() { # <json> <描述>
  local code
  code=$(envelope_code "$1")
  if [ "$code" = "0" ]; then ok "$2"; else bad "$2(code=${code:-无响应} body=$1)"; fi
}

expect_eq() { # <实际> <期望> <描述>
  if [ "$1" = "$2" ]; then ok "$3"; else bad "$3(实际=$1 期望=$2)"; fi
}

set_env_kv() { # <键> <值>:就地改 .env 里的一行(没有就追加)
  if grep -q "^$1=" .env 2>/dev/null; then
    sed -i.bak -E "s|^$1=.*|$1=$2|" .env && rm -f .env.bak
  else
    printf '%s=%s\n' "$1" "$2" >> .env
  fi
}

# ---------------------------------------------------------------- 启动判定

HAVE_DOCKER=0
if have docker && docker info >/dev/null 2>&1; then
  HAVE_DOCKER=1
fi

compose() { # 与 deploy/test/docker-compose.test.yml 的注释保持同一套用法
  docker compose -p yggauth-test --env-file .env.test \
    -f docker-compose.yml -f deploy/test/docker-compose.test.yml "$@"
}

# ================================================================ 步骤 1
step "步骤 1 / 7:起容器(真容器步骤)"
if [ "$GOLDEN_SKIP_DOCKER" = "1" ]; then
  skip "GOLDEN_SKIP_DOCKER=1 —— 直接用 $BASE_URL 上已在跑的服务"
elif [ "$HAVE_DOCKER" != "1" ]; then
  skip "本机没有可用的 Docker daemon —— 步骤 1 未执行(如实标注,不算通过)"
else
  if [ ! -f .env.test ]; then
    cp deploy/test/test.env.example .env.test
    info "已按 test.env.example 生成 .env.test(首次)"
  fi
  if compose up -d --build; then
    ok "compose up 完成"
  else
    bad "compose up 失败"
  fi
fi

# ================================================================ 步骤 2
step "步骤 2 / 7:看启动日志里的引导结果"
if [ "$HAVE_DOCKER" != "1" ] || [ "$GOLDEN_SKIP_DOCKER" = "1" ]; then
  skip "没有本机容器可看日志 —— 步骤 2 未执行"
else
  BOOT_LOG="$(compose logs app 2>/dev/null | grep -E 'bootstrap:' | tail -5)"
  if [ -n "$BOOT_LOG" ]; then
    ok "找到引导日志"
    printf '%s\n' "$BOOT_LOG" | sed 's/^/    /'
    if printf '%s' "$BOOT_LOG" | grep -qi 'created'; then
      info "首次引导创建了默认管理员(密码若为随机,已打印在上面这行日志里)"
    else
      info "已有管理员,引导按预期跳过 —— 不会复活被删掉的管理员(见 docs/configuration.md 5.1)"
    fi
    if [ -z "$ADMIN_PASSWORD" ] && printf '%s' "$BOOT_LOG" | grep -qi 'password'; then
      bad "日志里有密码提示,但没传 ADMIN_PASSWORD —— 把日志里的密码抄进环境变量再跑一遍"
    fi
  else
    bad "日志里没有 bootstrap: 行(引导没跑?日志级别?)"
  fi
fi

# ================================================================ 步骤 3
step "步骤 3:管理员登录,读得到受保护的后台数据"
if [ -z "$ADMIN_PASSWORD" ]; then
  bad "缺少 ADMIN_PASSWORD(.env 里留空时,随机密码只在启动日志里打印一次)"
else
  RESP=$(api POST /api/auth/login "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}")
  if [ "$(envelope_code "$RESP")" != "0" ]; then
    bad "登录失败: $RESP"
  else
    ok "登录成功"
    ACC=$(api GET /api/admin/accounts)
    CODE=$(envelope_code "$ACC")

    # 首登强制改密(must_change):除改密与认证路径外一律 20013。
    # 这里按设计先改一次密再继续 —— 不然后面每一步都会被同一条拦下。
    if [ "$CODE" = "20013" ]; then
      info "首登强制改密:先按设计改掉"
      NEW_PASS="${GOLDEN_NEW_PASSWORD:-${ADMIN_PASSWORD}9}"
      CH=$(api PATCH /api/account/password \
        "{\"old_password\":\"$ADMIN_PASSWORD\",\"new_password\":\"$NEW_PASS\"}")
      if [ "$(envelope_code "$CH")" = "0" ]; then
        ok "已改密(改密会吊销所有会话,重新登录)"
        ADMIN_PASSWORD="$NEW_PASS"
        RESP=$(api POST /api/auth/login \
          "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}")
        [ "$(envelope_code "$RESP")" = "0" ] && ok "重新登录成功" || bad "重新登录失败: $RESP"
        ACC=$(api GET /api/admin/accounts)
        CODE=$(envelope_code "$ACC")
      else
        bad "改密失败: $CH"
      fi
    fi

    expect_eq "$CODE" "0" "读取后台账号列表"
  fi
fi

# ================================================================ 步骤 4
step "步骤 4:改站点名 → 公开配置立刻变化(不重启)"
PUB=$(api GET /api/public/config)
BEFORE=$(json_field "$PUB" site_name)
info "改之前 site_name=$BEFORE"
RESP=$(api PATCH /api/admin/settings "{\"site.name\":\"$GOLDEN_SITE\"}")
expect_code0 "$RESP" "保存站点名"
PUB=$(api GET /api/public/config)
AFTER=$(json_field "$PUB" site_name)
expect_eq "$AFTER" "$GOLDEN_SITE" "公开配置已是新站点名(未重启)"
info "浏览器标签与后台左栏读的就是这个端点 —— 页面上人工再看一眼标题"

# ================================================================ 步骤 5
step "步骤 5:配 mail.* → 发测试信"
if [ -n "${GOLDEN_SMTP_HOST:-}" ]; then
  MAIL_JSON="{\"mail.transport\":\"smtp\",\"mail.host\":\"$GOLDEN_SMTP_HOST\",\"mail.port\":${GOLDEN_SMTP_PORT:-587},\"mail.user\":\"$GOLDEN_SMTP_USER\",\"mail.from\":\"${GOLDEN_SMTP_FROM:-noreply@localhost}\"}"
  [ -n "${GOLDEN_SMTP_PASSWORD:-}" ] && MAIL_JSON="${MAIL_JSON%,*},\"mail.password\":\"$GOLDEN_SMTP_PASSWORD\"}"
  info "用真实 SMTP:$GOLDEN_SMTP_HOST(收件箱应当真的收到信)"
else
  MAIL_JSON='{"mail.transport":"console"}'
  info "没给 GOLDEN_SMTP_HOST —— 用 console 模式(不真投递,验证的是链路不是收件箱)"
fi
RESP=$(api PATCH /api/admin/settings "$MAIL_JSON")
expect_code0 "$RESP" "保存发件配置(保存即重建发件器,不重启)"
TEST=$(api POST /api/admin/settings/mail/test "{\"to\":\"$GOLDEN_TO\"}")
if [ "$(envelope_code "$TEST")" = "0" ]; then
  STAGE=$(json_field "$TEST" stage)
  TRES=$(json_field "$TEST" transport)
  if [ "$(json_field "$TEST" ok)" = "true" ]; then
    ok "测试信通过(transport=$TRES stage=$STAGE)→ $GOLDEN_TO"
    NOTE=$(json_field "$TEST" note)
    [ -n "$NOTE" ] && info "提示:$NOTE"
  else
    bad "测试信失败(阶段=$STAGE):$(json_field "$TEST" error)"
    info "阶段含义:connect=地址/网络,tls=端口(465 要勾隐式 TLS),auth=账号密码,send=投递"
  fi
else
  bad "测试信接口报错: $TEST"
fi

# ================================================================ 步骤 6
step "步骤 6:关闭注册 → 三处一致(公开配置 / 策略 / 注册接口)"
RESP=$(api PATCH /api/admin/settings '{"registration.mode":"closed"}')
expect_code0 "$RESP" "保存 registration.mode=closed"
PUB=$(api GET /api/public/config)
expect_eq "$(json_field "$PUB" registration_mode)" "closed" "公开配置=closed"
POLICY=$(api GET /api/auth/policy)
expect_eq "$(json_field "$POLICY" registration_mode)" "closed" "策略端点=closed"
REG=$(api POST /api/auth/register \
  '{"username":"goldenpath","email":"goldenpath@example.com","password":"correct-horse-battery"}')
if [ "$(envelope_code "$REG")" = "20014" ]; then
  ok "注册接口被拒(20014 关闭注册)"
else
  bad "注册接口没有按关闭注册拒绝: $REG"
fi

# ================================================================ 步骤 7
step "步骤 7:改 .env 一个 L0 项 → 重启 → 生效且无差异告警"
if [ "$HAVE_DOCKER" != "1" ] || [ "$GOLDEN_SKIP_DOCKER" = "1" ]; then
  skip "没有本机容器 —— 步骤 7 未执行(如实标注,不算通过)"
else
  # 备份再改:验收脚本不该在别人的机器上留痕迹(退出时自动还原)
  cp .env .env.golden.bak
  ENV_BACKUP=.env.golden.bak
  OLD_LOG_LEVEL="$(grep -E '^LOG_LEVEL=' .env | cut -d= -f2)"
  OLD_LOG_LEVEL="${OLD_LOG_LEVEL:-info}"
  # info → warn 是纯 L0 改动:必须重启才生效,重启后 INFO 日志应当消失
  set_env_kv LOG_LEVEL warn
  compose up -d app >/dev/null 2>&1
  sleep 5
  LOGS="$(compose logs --since 30s app 2>/dev/null)"
  if printf '%s' "$LOGS" | grep -q 'INFO'; then
    bad "重启后仍有 INFO 日志 —— LOG_LEVEL 改动没生效"
  else
    ok "重启后生效(无 INFO 日志)"
  fi
  if printf '%s' "$LOGS" | grep -q '与 env 种子不同的键'; then
    bad "启动日志出现「与 env 种子不同的键」告警 —— 有键的现值和 .env 对不上"
  else
    ok "启动日志无 settings 差异告警"
  fi
  # 后台改过的键(settings 现值)应当在重启后依然是现值
  PUB=$(api GET /api/public/config)
  expect_eq "$(json_field "$PUB" site_name)" "$GOLDEN_SITE" "重启后站点名仍是 setting 的现值"
  set_env_kv LOG_LEVEL "$OLD_LOG_LEVEL"
fi

# ================================================================ 汇总
printf '\n\033[1m=== 结果\033[0m\n'
printf '  通过 %d,失败 %d,跳过 %d\n' "$PASS" "$FAIL" "$SKIPPED"
if [ "$SKIPPED" -gt 0 ]; then
  printf '  跳过的步骤(多半因为本机没有 Docker)**不算通过**:\n'
  printf '  需要补跑:把这份脚本拿到有 Docker 的机器上再执行一次。\n'
fi
[ "$FAIL" -eq 0 ] || exit 1
