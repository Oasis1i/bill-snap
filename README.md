# bill-snap

个人截图记账的 MVP。付完款后把收据截图上传，服务端用多模态模型抽出金额、商家、分类和时间，写入 SQLite，再用 HTTP 和 MCP 回答「这周外卖花了多少」。

Personal screenshot expense tracker. Upload a receipt image, extract bill fields, store them in SQLite, and query the result over HTTP or a small MCP server.

This repository is the skeleton: the mock extractor works offline, and the 通义 VL / 豆包 / 智谱 GLM-4V clients are thin OpenAI-compatible HTTP stubs. They are not chatbot subscriptions.

## 它做什么

1. `POST /api/v1/upload` 接收一张 png / jpeg / webp。
2. 抽取器判断是不是账单。不是账单就返回 JSON，**不插行、不留文件**。
3. 是账单则按去重规则写入 SQLite。同一笔支付的第二张截图返回原来的那一行。
4. `GET /api/v1/bills` 和 `GET /api/v1/summary` 按时间、分类查询。
5. MCP 用同一份 SQLite。本机 `cmd/mcp-server`（stdio）只读。远程 `POST /mcp` 用 `MCP_TOKEN` 读，用另一把 `MCP_WRITE_TOKEN` 才能调用 `add_bill`。两把都没设则该接口关闭。

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
| `UPLOAD_TOKEN` | 空 | 上传和查询必填。`Authorization: Bearer <token>` 或 `X-API-Token` |
| `MCP_TOKEN` | 空 | 只给 `POST /mcp` 的只读调用。`Authorization: Bearer <token>`。和 `MCP_WRITE_TOKEN` 都空则该接口 404。不要和 `UPLOAD_TOKEN` 用同一个值 |
| `MCP_WRITE_TOKEN` | 空 | `POST /mcp` 的写 token，能读也能调用 `add_bill`。必须和 `MCP_TOKEN` 不同，相同则进程拒绝启动。留空则没有 `add_bill`。stdio 不用这把 token |
| `DB_PATH` | `data/bills.db` | SQLite 文件，启动时创建 |
| `UPLOAD_DIR` | `data/uploads` | 账单截图目录。非账单不会写到这里 |
| `APP_TZ` | `Asia/Shanghai` | `YYYY-MM-DD` 查询按这个时区解释 |
| `MAX_UPLOAD_BYTES` | `8388608` | 单张上限，8 MiB |
| `VISION_PROVIDER` | `mock` | `mock`、`qwen`、`doubao`、`glm` |
| `VISION_API_KEY` | 空 | 真实供应商才需要 |
| `VISION_BASE_URL` | 按供应商 | 留空用下面的默认地址 |
| `VISION_MODEL` | 按供应商 | 豆包必须填 endpoint id |
| `CATEGORIES` | 内置十类 | 分类闭集。见「分类」。留空用默认 |

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
- `category`：精确匹配当前生效分类里的一项，例如 `外卖`。见「分类」。
- `limit`：只对列表有效，1 到 500，默认 100。
- 不带参数表示全部时间。

库存仍是「分」`amount_cents`。HTTP 账单对象同时给元，字段名 `amount`，两位小数字符串（`25.90`）。快捷指令直接显示这个字符串，不要再除以 100。`amount_cents` 留着给旧客户端。时间以 UTC RFC3339 存储，精度到分钟。

### HTTP 状态

| 状态 | 含义 |
| --- | --- |
| 201 | 新账单已写入。`stored: true` |
| 200 | 重复账单（`duplicate: true`）或不是账单（`is_bill: false`，`stored: false`） |
| 401 | 没有 token，或 token 不对 |
| 400 | 缺 `image` 字段，或不是 png/jpeg/webp |
| 413 | 超过大小限制 |
| 422 | 账单缺少支付时间，或金额为负 |
| 500 | 读库、存图或写库失败 |
| 502 | 抽取器失败（超时、结果无法解析、供应商报错，或 key / 模型没配好） |

失败时响应固定是两字段。`error` 是稳定机器码，`message` 是给手机通知的短中文短语：写到中文逗号为止，不加句号。没有堆栈、密钥或供应商原文：

```json
{"error":"unauthorized","message":"未授权"}
```

```json
{"error":"image_too_large","message":"图片超过上传大小限制"}
```

```json
{"error":"extract_timeout","message":"识别超时"}
```

识别失败的 `error`：`extract_timeout`（超时）、`extract_bad_response`（结果无法解析）、`extract_provider_error`（供应商报错）、`extract_not_configured`（未配置）、`extract_failed`（其他）。供应商原文只进服务器日志，并会截断、去掉密钥。上传还会见到 `bad_image_type`、`empty_image`、`missing_image`、`invalid_form`、`missing_paid_at`、`negative_amount`、`store_read_failed`、`image_save_failed`、`store_write_failed`，`message` 都是一句中文。列表和汇总失败也是这个形状。

失败时的 `message`，以及不是账单时的 `reason` / `notes`，都是短中文：写到第一个逗号为止，不加句号，也不含英文句子。图片类型不对是「图片格式不支持」，日期格式不对是「日期格式不对」。

不是账单时仍是 200。模型没写 `notes`，或写了英文（例如 “This is a screenshot, not a bill.”），`reason` 都是「这张图不是付款账单」。常见英文会收成更短的一句：「聊天截图」「主屏幕截图」「风景照片」「图片几乎是纯色」「图片太小」「图片无法解码」「图片是空的」。`notes` 与 `reason` 相同，不再回传抽取器原文。原文只进服务器日志。

```json
{"is_bill":false,"duplicate":false,"stored":false,"reason":"这张图不是付款账单","notes":"这张图不是付款账单"}
```

```json
{"is_bill":false,"duplicate":false,"stored":false,"reason":"图片几乎是纯色","notes":"图片几乎是纯色"}
```

快捷指令应把所有 2xx 当「请求被理解」，再看 `is_bill`，不要把「不是账单」当成网络错误。非 2xx 通知读 `message`；不是账单通知读 `reason`。

## 分类

分类是闭集，视觉模型不能另起标签。没设 `CATEGORIES` 时用内置的这十个，字符串必须逐字相同：

外卖、餐饮、交通、日用、购物、房租、工资、娱乐、医疗、其他

要改这张表，在环境变量或 `.env` 里设 `CATEGORIES`。分隔符用英文逗号 `,`，中文逗号 `，` 和顿号 `、` 同样认。每一项会去掉首尾空白。两种写法不要写在同一行里：

- **整表替换。** 写上要保留的全部标签。没写上的内置标签就去掉，多写的就是新增。漏了 `其他` 也会补上。
  `CATEGORIES=外卖,餐饮,交通,购物,宠物,其他`
- **在内置表上增减。** 每一项都带 `+` 或 `-`。`+宠物` 追加到末尾，`-工资` 从内置表删掉。从左到右生效。`-其他` 会报错，进程不启动。
  `CATEGORIES=+宠物,+订阅,-工资`

`其他` 始终留在生效列表里。不在这张表里的模型输出，包括空字符串、`null`、非字符串和相近写法（多几个字、中间夹空格、英文名），仍改成 `其他`。提示词和 MCP 工具 schema 里的 `enum` 用的是这一份生效列表。改完要重启进程。

入库走同一套归一。查询参数不做这步映射，仍然是精确匹配。旧账单上已经写下的分类不会改写；去掉某个标签之后，新截图不会再记成那个词，但按旧词查询仍然是精确匹配。

不加 CHECK，也不做迁移。

## iPhone 快捷指令

还没有 `.shortcut` 文件。Back Tap → 截图 → POST 可以以后再串。上传约定：

- URL：`https://<你的域名>/api/v1/upload`
- 方法：`POST`
- Header：`Authorization: Bearer <UPLOAD_TOKEN>`（或 `X-API-Token: <UPLOAD_TOKEN>`）
- Body：表单（Form），文件字段名必须是 `image`
- 类型：png、jpeg 或 webp

快捷指令里用「获取 URL 内容」：方法 POST，请求体选「表单」，字段 `image` 选刚才的截图。通知只拼两段，不要把 `items`、`source`、`note` 放进通知：

- 成功或重复：`bill.amount`（元，例如 `25.90`）和 `bill.category`（例如 `餐饮`）。不要读 `bill.merchant`，也不要读 `bill.amount_cents`。
- 不是账单：`reason`（短中文，例如 `这张图不是付款账单`）。
- 失败：`message`（短中文，例如 `未授权`、`识别超时`）。不要把整段 JSON 或 `error` 码展示给用户。

上传成功时 `bill.items` 是识别到的商品名（看不到就是空字符串），`bill.source` 是 `screenshot`。这两项只是响应里多出来的数据，手机通知仍然只有金额和类别。

成功时响应里金额长这样（其余字段仍在。`merchant` 留给存储和 MCP，通知不用。`raw_model_json` 不出现在 HTTP 里，原文留在库中）：

```json
{"is_bill":true,"duplicate":false,"stored":true,"bill":{"amount":"25.90","amount_cents":2590,"category":"餐饮","currency":"CNY","merchant":"沙县小吃"}}
```

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

4. **缺省支付时间，按本地日合并**。模型没给 `paid_at` 时，用上传时刻（再截断到分钟），库里的 `note` 仍附上 `paid_at missing; defaulted to upload time`，并把 `paid_at_defaulted` 记为 1。HTTP 返回给手机时这句改成「未识别到支付时间」，避免通知里出现英文。旧库回填仍认库里的英文原句。这一张的时间是缺省的，或者库里那一行是缺省的，只要金额、货币、归一化商家相同，并且支付日落在 `APP_TZ`（默认 `Asia/Shanghai`）的同一天，就视为同一笔。两边都有真实时间时，不会因为「同一天」并在一起。两边都有单号而且不一样时，也不走这一条，避免把同日同店同金额的两笔订单并掉。同一天有多笔候选时，命中 `id` 最小的那一笔。

已有数据库在启动时补列：`order_no`（可空）、`paid_at_defaulted`（整数，默认 0）、`items`（文本，默认空）、`source`（文本，默认 `screenshot`）、`request_id`（可空）。`CREATE TABLE IF NOT EXISTS` 不会给旧表加列，所以启动时看 `PRAGMA table_info`，缺了再 `ALTER TABLE`。旧行的 `note` 里如果有那句缺省说明，会把 `paid_at_defaulted` 回填成 1。`items` 和 `source` 只填默认值，不改金额、类别、时间和备注。从冷备份恢复的旧库同样在下次启动时补列。`request_id` 有部分唯一索引（`NULL` 可以有多行），给 `add_bill` 做幂等。手动记账不走上面的截图去重：`content_hash` 和 `dedup_key` 写成互不相同的 `manual:` 加随机值，这样唯一索引能收下每一笔。

`note` 继续只放系统说明（例如没识别到支付时间）和用户备注，不拿来存商品名。

不是账单：HTTP 200，`is_bill: false`，不插 `bills`，不写 `data/uploads/`。

## Mock 怎么判断

`VISION_PROVIDER=mock`（默认）不联网。规则按顺序：

1. 空文件，或无法解码的 png/jpeg/webp → 不是账单。
2. 宽或高小于 64 像素 → 不是账单。
3. 采样后的 8-bit 亮度标准差 `< 12` → 不是账单（纯色、近纯色、白屏）。
4. 否则是账单。字段只由图片 SHA-256 决定，同一文件永远得到同一结果：
   - 金额：`100 * (1 + hash[1] % 200)` 分，也就是 1.00 到 200.00 CNY
   - 商家 / 分类：`美团外卖/外卖`、`饿了么/外卖`、`瑞幸咖啡/餐饮`、`星巴克/餐饮`、`滴滴出行/交通`、`盒马鲜生/日用`、`淘宝/购物` 里按 `hash[2]` 取一项。这些分类都属于内置列表。若 `CATEGORIES` 去掉了其中某一项，这一项会记成 `其他`
   - 订单号：`M` 加上 SHA-256 前 6 字节的大写十六进制。每张图不同，所以 mock 不会靠订单号把两张不同的图并成一笔
   - 支付时间：`2026-01-01 12:00 UTC` 再加 `hash[0] % 365` 天。mock 总会给时间，所以 `paid_at_defaulted` 是 0

Mock 的日期不是「今天」。查询用 2026 年，或用上传响应里的 `paid_at`。`go run scripts/genpng.go` 造的彩色图会走第 4 条，白图会走第 3 条。

## 真实视觉接口

`qwen`、`doubao`、`glm`（以及 `tongyi`、`ark`、`zhipu` 这些别名）走同一个 `Extractor` 接口：`POST {VISION_BASE_URL}/chat/completions`，OpenAI 风格的 text + `image_url` data URL，Bearer key。提示词要求模型只回一个 JSON 对象，并且 `category` 只能是「分类」一节里的那几个字符串，拿不准就填 `其他`。`items` 是截图上能看清的商品或服务名，多个用「、」连接，看不到就留空，不要猜，也不要用商家名或类别代替。不是账单时 `items` 留空，`notes` 必须是一句短中文（聊天截图、空白图片、主屏幕截图、风景照片、这张图不是付款账单之一），不能写英文。就算模型仍回英文，接口也会改写成上面的短中文再给手机。商品名按字符截到 100 个。

这是可编译的薄客户端，**还没对着真实账号调通**。缺 key 时进程拒绝启动，避免静默落到一个坏配置。豆包还要自己的 endpoint id。供应商如果包一层 markdown，解析器会剥一层；若字段名不同，改 `internal/extract/openai.go` 里的 prompt 和 `visionPayload`。

## MCP

读和 HTTP 服务同一个 SQLite 文件。两套入口用同一份读工具定义。协议版本认 `2025-06-18`、`2025-03-26`、`2024-11-05`。金额是元，两位小数字符串（`25.90`）。时间按 `APP_TZ`（默认 Asia/Shanghai）显示成 `YYYY-MM-DD HH:MM`。列表不返回截图路径，也不返回模型原文。截图字节只出现在 `get_bill_image` 的结果里，没有单独的图片 URL。

读工具：

- `list_bills`：按日期列出明细。`from`、`to` 必填，`YYYY-MM-DD`，含首尾。`category` 可选，必须是当前分类之一。`limit` 可选，默认 50，超过 200 按 200。返回 `id`、`paid_at`、`amount`、`category`、`merchant`、`items`、`note`、`source`、`has_image`。`items` 是商品名，多个用「、」连接，看不到就是空字符串。`source` 是 `screenshot` 或 `manual`。`has_image` 为 true 的条件是：库里的 `image_path` 非空，并且该路径是普通文件，跟随符号链接之后仍然落在 `UPLOAD_DIR` 里面。路径为空、文件不在磁盘上、或解析后跑到上传目录外面（含 `../` 和指向外部的符号链接）时为 false。调用方看不到路径本身。
- `summarize_spend`：按日期汇总。`from`、`to` 必填。`group_by` 是 `category`（默认）或 `day`。`include_income` 默认 false，为 false 时不算 `工资`。返回总额 `amount`（元）、`count`、`groups`（每组有金额和笔数）。
- `list_categories`：无参数，返回当前生效的分类。
- `get_bill_image`：返回 `list_bills` 里某一笔的原始付款截图。参数只有 `id`（整数，必填，就是列表里的 `id`，最小 1）。成功时 `content` 有两项，顺序固定：
  - `image`：`data` 是标准 base64，`mimeType` 是 `image/png` 或 `image/jpeg`。最长边缩到不超过 1024 像素，保持宽高比，已经不超过 1024 的不放大。原来是 PNG 的仍输出 PNG，原来是 JPEG 的仍输出 JPEG。WebP 没有透明通道时输出 JPEG，有透明通道时输出 PNG。不超过 1024 且本身就是 PNG 或 JPEG 的，按原字节返回。
  - `text`：一行 `YYYY-MM-DD 分类 金额 元`，例如 `2026-10-30 外卖 25.90 元`。日期按 `APP_TZ`。
  失败是工具结果里的 `isError: true`，正文只有一句短中文：`参数 id 无效`（缺失、不是整数、或小于 1）、`找不到这笔账单`、`这笔没有截图`（路径为空）、`截图文件已丢失`（文件不存在，或解析后不在 `UPLOAD_DIR` 内）、`截图无法读取`（不是能解码的图片，或大到不能安全解码）。单边超过 32768 像素、或总像素超过 2000 万的文件会走最后这一句，避免把超大图整张解进内存。

- `add_bill`：手动记一笔（现金、没有截图的消费、工资、房租）。只出现在写 token 的 `tools/list` 里。调用前先向用户复述并得到确认。参数：
  - `amount`（必填）：元，字符串或数字都行，例如 `"25.9"`、`25.9`、`"8000"`。必须大于 0、最多两位小数、不超过 1000000。
  - `category`（必填）：必须是当前配置的分类，不做近义词改写。
  - `merchant`（可选）：最多 50 字。
  - `paid_at`（可选）：RFC3339，或 `YYYY-MM-DD`，或 `YYYY-MM-DD HH:MM`，按 `APP_TZ`（默认 Asia/Shanghai）。只有日期时取当天 12:00。缺省是现在。不能早于 2000-01-01，也不能晚于现在加 1 天。
  - `items`（可选）：商品名，多个用「、」，超过 100 字按字符截断。
  - `note`（可选）：备注，最多 200 字。
  - `request_id`（可选）：幂等键，最多 64 字符。同一个键再调用一次，返回原来的那笔，`created` 为 false，不改字段。
  不走截图去重。没传 `request_id` 时照常保存；如果同一天已经有相同金额和类别，响应里带一句中文 `warning`，账单仍然写入。工资仍然受 `summarize_spend` 的 `include_income` 控制，默认不计入。成功时金额是元字符串，例如 `8000.00`。校验失败是 `isError: true` 和一句短中文，例如 `金额格式不对`、`金额必须大于0`、`金额最多两位小数`、`金额超过上限`、`类别应为：…`、`时间格式应为 2026-10-30 或 2026-10-30 12:30`、`时间超出允许范围`、`商家最多50字`、`备注最多200字`、`request_id 过长`、`不认识的参数：foo`、`保存失败，请稍后再试`。

日期必须是 `YYYY-MM-DD`。开始晚于结束、跨度超过 366 天（含首尾）、分类不在列表里，都是工具结果里的 `isError: true`，正文是短中文，没有句号，也没有逗号后面的半句。未知分类会带上允许的列表。协议错误（解析失败、方法不存在）走 JSON-RPC `error`。

### 谁能写

| 调用方 | `tools/list` | 调用 `add_bill` |
| --- | --- | --- |
| `MCP_TOKEN` | 4 个读工具 | `没有写入权限` |
| `MCP_WRITE_TOKEN` | 4 个读工具 + `add_bill` | 允许 |
| 没设 `MCP_WRITE_TOKEN` | 只有读工具 | `写入功能未开启` |
| 没带 token 或 token 不对 | 401 | 401 |

两把 token 都用常时间比较。启动时如果 `MCP_WRITE_TOKEN` 和 `MCP_TOKEN` 相同，进程直接退出，避免配成同一把。写 token 同时能读，助手只配这一把即可。stdio 不看这两把 token，也永远不提供 `add_bill`。

每次 `add_bill`（成功和失败）都往 `audit_log` 插一行：时间、token 种类（`read` / `write` / `stdio`）、工具名、原始参数 JSON、结果（`ok` 或 `error:` 加中文原因）、账单 id。同一条会打到进程 stderr，systemd 的 journald 会收下。日志里只有 token 种类，没有 token 本身。审计表只增不改，MCP 不暴露它。

### 给旧账补商品名

`cmd/backfill-items` 找出 `items` 仍为空、并且 `image_path` 指向文件的行，用当前视觉模型重跑识别，只准备写回 `items`。默认是 dry-run，只打印将要写入的商品名。确认后再加 `-apply`。`-apply` 不改金额、类别、时间、备注、图片路径和 `updated_at`。图片不存在、不是账单、或没认出商品名的行会跳过。

数据库和模型配置与主服务相同（`.env` 里的 `DB_PATH`、`APP_TZ`、`VISION_*`）。相对路径按当前工作目录解析，要在仓库根目录跑，或者和上传时用同一个工作目录。

```bash
go run ./cmd/backfill-items
go run ./cmd/backfill-items -apply
```

### 远程：`POST /mcp`

挂在主 HTTP 进程上，Streamable HTTP，无状态，不发 `Mcp-Session-Id`。普通 JSON 即可。只接受 `application/json`。`GET /mcp` 是 405。

`MCP_TOKEN` 和 `MCP_WRITE_TOKEN` 都没设时，这个路径直接 404，不会匿名开放。设了之后只认 `Authorization: Bearer`，不认 `X-API-Token`，也不认 `UPLOAD_TOKEN`。读 token 和写 token 都能通过鉴权；缺失或错误是 401。写操作只认写 token。

这个进程本身仍是 HTTP。公网 HTTPS 由前面的 nginx 终止，不在这里配证书。nginx 要把 `Authorization` 原样转给上游。

```bash
curl -sS -X POST "http://127.0.0.1:8080/mcp" \
  -H "Authorization: Bearer $MCP_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'

curl -sS -X POST "http://127.0.0.1:8080/mcp" \
  -H "Authorization: Bearer $MCP_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"summarize_spend","arguments":{"from":"2026-10-01","to":"2026-10-31"}}}'
```

Cursor 或其他支持远程 MCP 的客户端可以这样接（公网把地址换成 nginx 上的 `https://` 主机名）：

```json
{
  "mcpServers": {
    "bill-snap": {
      "url": "http://127.0.0.1:8080/mcp",
      "headers": {
        "Authorization": "Bearer <MCP_TOKEN>"
      }
    }
  }
}
```

### 本机 stdio

`cmd/mcp-server` 仍是一行一条 JSON-RPC，并且只提供 4 个读工具。日志在 stderr。它**不校验** `UPLOAD_TOKEN`、`MCP_TOKEN` 或 `MCP_WRITE_TOKEN`：能启动这个进程的人已经能读数据库文件，但这个进程不能 `add_bill`。要手动记账，走 `POST /mcp` 的写 token。截图和远程接口用同一个 `UPLOAD_DIR`（默认 `data/uploads`）。相对路径按进程的工作目录解析，要和上传时一致；目录用绝对路径更稳。

```bash
export DB_PATH=data/bills.db
export APP_TZ=Asia/Shanghai
go run ./cmd/mcp-server
# 或
make mcp
```

Nori / Coin 以后只要支持自定义 MCP stdio，配成类似这样即可（路径改成你机器上的绝对路径）。本仓库没有这两款助手的专用插件。

```json
{
  "mcpServers": {
    "bill-snap": {
      "command": "/absolute/path/to/bin/bill-snap-mcp",
      "env": {
        "DB_PATH": "/absolute/path/to/data/bills.db",
        "UPLOAD_DIR": "/absolute/path/to/data/uploads",
        "APP_TZ": "Asia/Shanghai"
      }
    }
  }
}
```

助手问「这周外卖花了多少」时，应调用 `summarize_spend`，`from` / `to` 填本周日期，再看 `groups` 里的 `外卖`。默认不含工资。`list_bills` 的 `category` schema 是当前生效的那份列表，不能填列表以外的词。要看某一笔的截图时，先 `list_bills`，对 `has_image` 为 true 的行再调 `get_bill_image`，`id` 用列表里的编号。

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
- 截图是私人付款记录。没有图片下载 URL；文件只在服务器磁盘上。`get_bill_image` 走原来的 `POST /mcp`，远程仍要 `MCP_TOKEN`。读文件前会 `EvalSymlinks`，再用 `filepath.Rel` 确认结果仍在 `UPLOAD_DIR` 内。目录外的路径和逃逸的符号链接都不会打开。
- 默认只听 `127.0.0.1`。改成 `:8080` 之前先换掉 `change-me`。
- 不要把 `.env`、`data/` 或 API key 提交进 git。冷备份只上传 SQLite 快照。
- 远程 MCP 在 `MCP_TOKEN` 或 `MCP_WRITE_TOKEN` 至少设了一把之后出现，并且只认对应的 Bearer。不要把它们和 `UPLOAD_TOKEN` 设成同一个值，两把 MCP token 也不能相同，也不要无鉴权地暴露 `POST /mcp`。写 token 只能新增，不能改、不能删。
- 本机 `cmd/mcp-server` 是 stdio，不校验 token，也没有写工具。不要把它无鉴权地转到网络上；要走网络就用 `POST /mcp`。

## 目录

```text
cmd/server/            HTTP API，含 POST /mcp
cmd/mcp-server/        MCP stdio（只读）
cmd/backfill-items/    给旧截图补 items，默认不写库
internal/api/          路由、鉴权、上传和查询
internal/mcp/          MCP 工具、stdio 和 Streamable HTTP
internal/backfill/     只更新 items 的补数据
internal/store/        SQLite。schema 的嵌入副本
internal/extract/      Extractor、mock、通义/豆包/智谱客户端
internal/config/       环境变量和 .env
internal/model/        金额、分类、去重键、订单号、时间范围
schema.sql             与 internal/store/schema.sql 保持一致
.env.example
scripts/smoke.sh         curl + MCP 冒烟
scripts/backup-drive.sh  SQLite 快照冷备份到 Google Drive（rclone）
scripts/restore-drive.sh 本地库缺失或为空时从 Drive 拉回
scripts/drive-env.sh     上面两个脚本共用的环境变量
Makefile
```

`go build ./...` 应能通过。`make test` 覆盖去重、非账单丢弃、鉴权和 MCP。`make smoke` 对一个临时 SQLite 文件做真实 curl。
