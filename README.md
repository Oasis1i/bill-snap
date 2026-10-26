# bill-snap

个人截图记账的 MVP。付完款后把收据截图上传，服务端用多模态模型抽出金额、商家、分类和时间，写入 SQLite，再用 HTTP 和 MCP 回答「这周外卖花了多少」。

Personal screenshot expense tracker. Upload a receipt image, extract bill fields, store them in SQLite, and query the result over HTTP or a small MCP server.

This repository is the skeleton: the mock extractor works offline, and the 通义 VL / 豆包 / 智谱 GLM-4V clients are thin OpenAI-compatible HTTP stubs. They are not chatbot subscriptions.

## 它做什么

1. `POST /api/v1/upload` 接收一张 png / jpeg / webp。
2. 抽取器判断是不是账单。不是账单就返回 JSON，**不插行、不留文件**。
3. 是账单则按去重规则写入 SQLite。同一笔支付的第二张截图返回原来的那一行。
4. `GET /api/v1/bills` 和 `GET /api/v1/summary` 按时间、分类查询。
5. `cmd/mcp-server` 用同一份 SQLite 暴露 `list_bills` 和 `summarize_spend`，给 Nori / Coin 以后接入。

所有会碰到账单数据的 HTTP 接口都要 token。`/healthz` 不需要。

## 本地运行

需要 Go 1.22 或更新。SQLite 用纯 Go 驱动 `modernc.org/sqlite`，不用 CGO，也不用 Docker。

```bash
cp .env.example .env
# 把 UPLOAD_TOKEN 改成一长串随机字符串

go run ./cmd/server
# 或
make run
```

进程会读取工作目录下的 `.env`，但**不会覆盖**已经存在的环境变量。`.env` 不要提交。

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | 只监听本机。手机走局域网时改成 `:8080` |
| `PORT` | 空 | 没设 `HTTP_ADDR` 时，听 `:$PORT` |
| `UPLOAD_TOKEN` | 空 | HTTP 必填。`Authorization: Bearer <token>` 或 `X-API-Token` |
| `DB_PATH` | `data/bills.db` | SQLite 文件，启动时创建 |
| `UPLOAD_DIR` | `data/uploads` | 账单截图目录。非账单不会写到这里 |
| `APP_TZ` | `Asia/Shanghai` | `YYYY-MM-DD` 查询按这个时区解释 |
| `MAX_UPLOAD_BYTES` | `8388608` | 单张上限，8 MiB |
| `VISION_PROVIDER` | `mock` | `mock`、`qwen`、`doubao`、`glm` |
| `VISION_API_KEY` | 空 | 真实供应商才需要 |
| `VISION_BASE_URL` | 按供应商 | 留空用下面的默认地址 |
| `VISION_MODEL` | 按供应商 | 豆包必须填 endpoint id |

默认地址（可用 `VISION_BASE_URL` / `VISION_MODEL` 覆盖，上线前请按控制台核对）：

- 通义 VL：`https://dashscope.aliyuncs.com/compatible-mode/v1`，模型 `qwen-vl-plus`
- 智谱 GLM-4V：`https://open.bigmodel.cn/api/paas/v4`，模型 `glm-4v-plus`
- 豆包 / 火山方舟：`https://ark.cn-beijing.volces.com/api/v3`，`VISION_MODEL` 填 `ep-...`，没有默认模型

`make build` 会生成 `bin/bill-snap` 和 `bin/bill-snap-mcp`。

### 用 curl 打一遍

另开一个终端。先造一张「像账单」的图和一张空白图（mock 规则见下文）：

```bash
go run scripts/genpng.go /tmp/bill.png /tmp/blank.png

curl -sS -X POST "http://127.0.0.1:8080/api/v1/upload" \
  -H "Authorization: Bearer $UPLOAD_TOKEN" \
  -F "image=@/tmp/bill.png;type=image/png"

curl -sS "http://127.0.0.1:8080/api/v1/bills?from=2026-01-01&to=2026-12-31" \
  -H "Authorization: Bearer $UPLOAD_TOKEN"

curl -sS "http://127.0.0.1:8080/api/v1/summary?from=2026-10-20&to=2026-10-26&category=外卖" \
  -H "X-API-Token: $UPLOAD_TOKEN"
```

也可以 `make test` 和 `make smoke`。smoke 会起一个临时服务，用 curl 上传、查汇总，再跑一遍 MCP。

### 查询参数

- `from`、`to`：`YYYY-MM-DD`（按 `APP_TZ` 的整天，含首尾）或 RFC3339 时刻（`from` 含，`to` 含该秒）。
- `category`：精确匹配，例如 `外卖`。
- `limit`：只对列表有效，1 到 500，默认 100。
- 不带参数表示全部时间。

金额以「分」存在 `amount_cents`，同时给十进制字符串 `amount`（`18.50`）。时间以 UTC RFC3339 存储，精度到分钟。

### HTTP 状态

| 状态 | 含义 |
| --- | --- |
| 201 | 新账单已写入。`stored: true` |
| 200 | 重复账单（`duplicate: true`）或不是账单（`is_bill: false`，`stored: false`） |
| 401 | 没有 token，或 token 不对 |
| 400 | 缺 `image` 字段，或不是 png/jpeg/webp |
| 413 | 超过大小限制 |
| 502 | 抽取器失败（例如真实供应商的 key / 模型还没填好） |

不是账单时的响应类似：

```json
{"is_bill":false,"duplicate":false,"stored":false,"reason":"image is nearly a solid color, so it was not treated as a receipt"}
```

快捷指令应把所有 2xx 当「请求被理解」，再看 `is_bill`，不要把「不是账单」当成网络错误。

## iPhone 快捷指令

还没有 `.shortcut` 文件。Back Tap → 截图 → POST 可以以后再串。上传约定：

- URL：`https://<你的域名>/api/v1/upload`
- 方法：`POST`
- Header：`Authorization: Bearer <UPLOAD_TOKEN>`（或 `X-API-Token: <UPLOAD_TOKEN>`）
- Body：表单（Form），文件字段名必须是 `image`
- 类型：png、jpeg 或 webp

快捷指令里用「获取 URL 内容」：方法 POST，请求体选「表单」，字段 `image` 选刚才的截图。成功后读 JSON 里的 `bill.merchant`、`bill.amount`，或者非账单的 `reason`。

同一张图传两次是安全的，第二次会带回已有记录。

## 去重和非账单

命中任意一道都返回已有行，不更新字段，也不再存第二张图。按下面的顺序检查。

1. **内容哈希**。`content_hash = hex(SHA-256(图片原始字节))`，唯一索引。同一张截图重复上传直接命中，哪怕以后换了模型也不会另插一行。

2. **订单号** `order_no`。提示词要求模型在看到订单号、交易单号、商户单号、订单编号、交易号或商户订单号时填写；多张单号同时出现时优先交易单号，其次订单号，再次商户单号。看不到就留空。入库前归一化：去掉空白、连字符、下划线、冒号、点号、斜杠和井号，全角数字和字母折成半角，ASCII 字母大写。`无`、`未知`、`暂无`、`none`、`null`、`n/a` 当成没有单号。归一化后非空才参与比较，列上有部分唯一索引（`NULL` 可以有多行）。单号相同就是同一笔，不再看金额、商家和时间。

3. **支付键** `dedup_key`，唯一索引。材料是 UTF-8 字符串的 SHA-256 十六进制，和以前一样：

   ```text
   <amount_cents>|<CURRENCY>|<normalized merchant>|<paid_at>
   ```

   - 金额是分，货币大写（空则 `CNY`）
   - 商家去掉首尾空白、合并中间空白、ASCII 小写（中文不变）
   - `paid_at` 先转到 UTC，再截断到分钟，格式 RFC3339，例如 `2026-10-26T04:09:00Z`

   同一分钟里、同一商家、同一金额的两张不同截图会合成一笔。商家为空时，这个键会把「同分钟同金额」并在一起；mock 总会填商家，真实模型也应该填。没有订单号的行一直用这套材料，已经写进库里的 `dedup_key` 不用重算。

   两边都有单号而且不一样时，不用这一条把它们并掉。若这个支付键已经属于另一张单，第二笔改存另一个键：在上面的材料后面再加 `|<normalized order_no>` 再做 SHA-256。

4. **缺省支付时间，按本地日合并**。模型没给 `paid_at` 时，用上传时刻（再截断到分钟），`note` 里附上 `paid_at missing; defaulted to upload time`，并把 `paid_at_defaulted` 记为 1。这一张的时间是缺省的，或者库里那一行是缺省的，只要金额、货币、归一化商家相同，并且支付日落在 `APP_TZ`（默认 `Asia/Shanghai`）的同一天，就视为同一笔。两边都有真实时间时，不会因为「同一天」并在一起。两边都有单号而且不一样时，也不走这一条，避免把同日同店同金额的两笔订单并掉。同一天有多笔候选时，命中 `id` 最小的那一笔。

已有数据库在启动时补两列：`order_no`（可空）、`paid_at_defaulted`（整数，默认 0）。`CREATE TABLE IF NOT EXISTS` 不会给旧表加列，所以启动时看 `PRAGMA table_info`，缺了再 `ALTER TABLE`。旧行的 `note` 里如果有那句缺省说明，会把 `paid_at_defaulted` 回填成 1。从冷备份恢复的旧库同样在下次启动时补列。

不是账单：HTTP 200，`is_bill: false`，不插 `bills`，不写 `data/uploads/`。

## Mock 怎么判断

`VISION_PROVIDER=mock`（默认）不联网。规则按顺序：

1. 空文件，或无法解码的 png/jpeg/webp → 不是账单。
2. 宽或高小于 64 像素 → 不是账单。
3. 采样后的 8-bit 亮度标准差 `< 12` → 不是账单（纯色、近纯色、白屏）。
4. 否则是账单。字段只由图片 SHA-256 决定，同一文件永远得到同一结果：
   - 金额：`100 * (1 + hash[1] % 200)` 分，也就是 1.00 到 200.00 CNY
   - 商家 / 分类：`美团外卖/外卖`、`饿了么/外卖`、`瑞幸咖啡/餐饮`、`星巴克/餐饮`、`滴滴出行/交通`、`盒马鲜生/日用`、`淘宝/购物` 里按 `hash[2]` 取一项
   - 订单号：`M` 加上 SHA-256 前 6 字节的大写十六进制。每张图不同，所以 mock 不会靠订单号把两张不同的图并成一笔
   - 支付时间：`2026-01-01 12:00 UTC` 再加 `hash[0] % 365` 天。mock 总会给时间，所以 `paid_at_defaulted` 是 0

Mock 的日期不是「今天」。查询用 2026 年，或用上传响应里的 `paid_at`。`go run scripts/genpng.go` 造的彩色图会走第 4 条，白图会走第 3 条。

## 真实视觉接口

`qwen`、`doubao`、`glm`（以及 `tongyi`、`ark`、`zhipu` 这些别名）走同一个 `Extractor` 接口：`POST {VISION_BASE_URL}/chat/completions`，OpenAI 风格的 text + `image_url` data URL，Bearer key。提示词要求模型只回一个 JSON 对象。

这是可编译的薄客户端，**还没对着真实账号调通**。缺 key 时进程拒绝启动，避免静默落到一个坏配置。豆包还要自己的 endpoint id。供应商如果包一层 markdown，解析器会剥一层；若字段名不同，改 `internal/extract/openai.go` 里的 prompt 和 `visionPayload`。

## MCP

和 HTTP 服务读同一个 SQLite 文件，stdio，一行一条 JSON-RPC（MCP `2024-11-05`，也认 `2025-03-26` 和 `2025-06-18`）。日志在 stderr。MCP **不校验** `UPLOAD_TOKEN`：能启动这个进程的人已经能读数据库文件。

```bash
export DB_PATH=data/bills.db
export APP_TZ=Asia/Shanghai
go run ./cmd/mcp-server
# 或
make mcp
```

工具：

- `list_bills`：`from`、`to`、`category`、`limit`
- `summarize_spend`：`from`、`to`、`category` → `count`、`totals`、`by_category`

Nori / Coin 以后只要支持自定义 MCP stdio，配成类似这样即可（路径改成你机器上的绝对路径）。本仓库没有这两款助手的专用插件。

```json
{
  "mcpServers": {
    "bill-snap": {
      "command": "/absolute/path/to/bin/bill-snap-mcp",
      "env": {
        "DB_PATH": "/absolute/path/to/data/bills.db",
        "APP_TZ": "Asia/Shanghai"
      }
    }
  }
}
```

助手问「这周外卖花了多少」时，应调用 `summarize_spend`，`from` / `to` 填本周日期，`category` 填 `外卖`。

## 部署（只是轮廓）

不要把带默认 token 的进程直接暴露到公网。

一台便宜 VPS：把 `bin/bill-snap` 放上去，用 systemd 的 `EnvironmentFile=` 注入 token 和路径，前面再加 Caddy 或 nginx 做 HTTPS。数据目录单独放，并限制文件权限。

家里的机器不想开端口：用 Cloudflare Tunnel 把本机 `127.0.0.1:8080` 转到一个 HTTPS 主机名，快捷指令访问那个名字。Token 仍然放在 Header 里，不要写进 URL。

```bash
cloudflared tunnel --url http://127.0.0.1:8080
```

这只是以后的步骤，这个 MVP 没有替你部署。

## Google Drive 冷备份

本地 SQLite（`DB_PATH`）是事实来源。Google Drive 只做单向冷备份：上传一份一致的数据库快照，并在本机库缺失或没有账单时拉回来。

备份由这台机器上的 cron 执行 `scripts/backup-drive.sh`。这套 cron 替换任何 Grok Bot 定时任务；不需要外部 bot 常驻。

脚本只把 SQLite 快照交给 rclone。不会上传 `.env`、`UPLOAD_TOKEN` 或 `VISION_API_KEY`。rclone 的 Google 凭证留在 rclone 自己的配置文件里，不要写进仓库。

### 一次性配置 rclone

安装 [rclone](https://rclone.org/)，然后：

```bash
rclone config
```

新建一个 Google Drive remote，名字与 `RCLONE_REMOTE` 相同（默认 `gdrive`）。Storage 选 `drive`。登录已经拥有 `bill-snap/latest/` 和 `bill-snap/backups/` 的那个 Google 账号。Scope 选完整 Drive 访问（`drive`），rclone 才能看到以前由别的客户端建好的目录。`drive.file` 只能看见这个 rclone 自己创建的文件。

目录不存在时，脚本会创建 `DRIVE_BACKUP_ROOT/latest` 和 `DRIVE_BACKUP_ROOT/backups`（默认根目录 `bill-snap`）。

### 备份

`scripts/backup-drive.sh`（或 `make backup-drive`）：

1. 定位仓库根目录，读取 `.env` 里尚未在环境中设置的 `DB_PATH`、`RCLONE_REMOTE`、`DRIVE_BACKUP_ROOT`、`APP_TZ`、`DRIVE_BACKUP_KEEP`。
2. 要求本机有 `sqlite3` 和 `rclone`。
3. 对正在使用的库做 WAL checkpoint，再用 `sqlite3 .backup` 写到临时文件。上传的是这份快照。
4. 覆盖写到 `<RCLONE_REMOTE>:<DRIVE_BACKUP_ROOT>/latest/bills.db`。
5. 再写一份 `backups/bills-YYYYMMDD-HHMM.db`。时间用 `APP_TZ`（默认 `Asia/Shanghai`），精确到分钟。同一分钟内再次运行会覆盖这一份。
6. 默认只保留最新 14 份符合 `bills-YYYYMMDD-HHMM.db` 的对象，按文件名排序删除更老的。`DRIVE_BACKUP_KEEP=0` 表示不删除。Google Drive 上 rclone 默认把删除放进回收站（`drive-use-trash`）。其他文件名不会被删。

成功时打印一行，里面有账单条数和两个远程路径。任一步失败都以非零状态退出。

### 恢复

换机器、重装、或本地 `data/bills.db` 丢了之后，确认这台机器的 rclone 用的是同一个 Google 账号，然后：

```bash
scripts/restore-drive.sh
# 或
make restore-drive
```

本地文件不存在、是空文件、或 `bills` 表里没有行时，从 `latest/bills.db` 下载到 `DB_PATH`。已经有账单时脚本直接退出，避免盖掉数据。确定要覆盖时：

```bash
scripts/restore-drive.sh --force
```

`--force` 会先把当前非空文件复制为 `DB_PATH.before-restore`（下次恢复会覆盖这份副本）。如果 `bill-snap` 或 MCP 进程还开着这个库，脚本会警告并拒绝写入。本机没有 `lsof` 或 `fuser` 时做不了这个检查，脚本会提示先停掉进程。先停掉这两个进程再恢复。

### crontab

每天 08:00、14:00、20:00（Asia/Shanghai）跑一次。`CRON_TZ` 决定触发时刻；快照文件名仍由 `APP_TZ` 决定。把路径换成这台机器上的绝对路径。`data/` 需要已存在（服务启动时会创建，也可以自己 `mkdir -p`）：

```cron
CRON_TZ=Asia/Shanghai
0 8,14,20 * * * /absolute/path/to/bill-snap/scripts/backup-drive.sh >> /absolute/path/to/bill-snap/data/backup-drive.log 2>&1
```

脚本根据自己的路径找仓库根目录并读取那里的 `.env`，不依赖 cron 的当前目录。日志放在 `data/` 下，这个目录已被 git 忽略。主机时区已经是 Asia/Shanghai 时，可以不写 `CRON_TZ`。`CRON_TZ` 适用于 cronie / Vixie cron。

## 安全

- 每个会读或写账单的 HTTP 接口都要 token。错 token 或缺失都是 401。
- 截图是私人付款记录。没有不带鉴权的图片下载路由；文件只在服务器磁盘上。
- 默认只听 `127.0.0.1`。改成 `:8080` 之前先换掉 `change-me`。
- 不要把 `.env`、`data/` 或 API key 提交进 git。冷备份只上传 SQLite 快照。
- MCP 是本机 stdio。不要把它无鉴权地转到网络上。

## 目录

```text
cmd/server/            HTTP API
cmd/mcp-server/        MCP stdio
internal/api/          路由、鉴权、上传和查询
internal/store/        SQLite。schema 的嵌入副本
internal/extract/      Extractor、mock、通义/豆包/智谱客户端
internal/config/       环境变量和 .env
internal/model/        金额、去重键、订单号、时间范围
schema.sql             与 internal/store/schema.sql 保持一致
.env.example
scripts/smoke.sh         curl + MCP 冒烟
scripts/backup-drive.sh  SQLite 快照冷备份到 Google Drive（rclone）
scripts/restore-drive.sh 本地库缺失或为空时从 Drive 拉回
scripts/drive-env.sh     上面两个脚本共用的环境变量
Makefile
```

`go build ./...` 应能通过。`make test` 覆盖去重、非账单丢弃、鉴权和 MCP。`make smoke` 对一个临时 SQLite 文件做真实 curl。
