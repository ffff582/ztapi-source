# Xcloud 本轮受控验收工具

本命令仅支持 `--stage`（免费报价及目录核验、私有暂存）和 `--verify-paid`（两种响应模式的上游探测）。没有迁移、发布、回滚、改渠道、改客户余额或客户 Key 的入口。

## 运行条件

- 仅在 Linux/WSL 运行；使用 `SQL_DSN`、`ZTAPI_UPSTREAM_MASTER_KEY` 和 `XCLOUD_ACCEPTANCE_KEY_FILE`，不得把密钥或 DSN 放到参数或 JSON 中。
- Key、预算凭证及所在目录必须是当前用户所有的私有普通文件/目录，不能经过符号链接。文件权限仅限当前用户。
- stdin 为严格 JSON，最多 32 KiB；字段名、模型、渠道、分组、当前配置版本和采购源必须精确匹配。仅允许本批次 `xcloud-sync-20261128` 和现有管理员验收身份。
- 模型仍是私有草稿；暂存不代表上架。真实探测使用临时凭证的内存副本，不会写回生产渠道。

## 预算凭证

付费模式须先在独立预算账本中占用额度，再生成受保护的凭证文件。输入的 `reservation_file` 和 `reservation_sha256` 必须对应该文件的完整原始字节。

凭证需要与请求完全一致的 `batch_id`、`attempt_id`、`operator_id`、`source_model`、`channel_id`、`group`、`expected`、`key_reference_id`、`limit_proof_id`、`limit_proof_sha256`，以及：

- `credential_sha256`：实际将发到上游的 Key 字节的 SHA256，不是文件名或未核对的 Key 引用；不保存 Key 明文。
- `claim_directory`：预算凭证固定的私有一次性锁目录。复制凭证、移动 Key 或更换证据目录不会改变锁的位置；不得删除或自动重置已经存在的锁。
- `reservation_id`、`state: "reserved"`、`cap_u`、`batch_limit_u: "10"`、`per_model_limit_u: "1"`、RFC3339 格式的 `reserved_at`、`expires_at`。

`cap_u` 必须是大于 0、不超过 1 的十进制字符串。全批累计额度由外部持久预算账本负责，本命令不是供应商扣费硬限额，输出会明确保留 `supplier_billing_cap_enforced: false`。上游 Key 的真实额度、分组、型号白名单及过期时间要另行核实。

## 回执与失败

每次尝试创建新证据目录，先落盘 `request.json` 和一次性锁，再执行；最终原子归档 `result.json`。相同预算凭证不能因改 Key 文件目录而重复执行。文件中仅保留安全的请求编号、原始计数、HTTP 状态及结果状态，不保存正文或任意上游字段。

失败、断流、数据库写入失败、进程中断或归档失败都不自动重试，也不释放未知费用预留。写库失败的探测回执仍需归档，未持久化的 verification ID 为 0，不能据此发布或捏造财务事实。

探测通过不等于完整客户端、工具续调或平台实际结算通过。供应商实扣与报价估算分开，财务归集、完整同提交 CI、真实平台验收及受保护成本证明仍是独立发布门槛。
