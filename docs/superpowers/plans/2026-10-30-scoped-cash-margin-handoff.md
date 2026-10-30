# 三型号现金毛利版本交接

## 边界

仅 Gemini 3 Flash Preview、Gemini 3.1 Pro Preview、Kimi K2.7 Code；不扩展其他型号，不改变旧 policy 公式，不执行生产写入、付费调用或部署。

| 型号 | 原采购源参考 | 原 resource 保留 | 发布渠道必须精确等于 | 新 policy | 目标毛利 |
| --- | ---: | --- | --- | --- | ---: |
| zt-gemini-3-flash-preview | 1386 | enterprise | [1] | cash_enterprise_20_20261030 | 20% |
| zt-gemini-3.1-pro-preview | 1387 | enterprise | [2] | cash_pool_30_20261030 | 30% |
| zt-kimi-k2.7-code | 1393 | pool | [2] | cash_pool_30_20261030 | 30% |

数字 source ID 是用户提供的生产参考，不在实现中硬编码；请求的 model/version/snapshot 基线将实际绑定价格源锁定。若发布前读到不同绑定，必须停止并重新审阅预览，不得套用这份旧基线。

采购标签不是实际销售渠道。Pro 保留 enterprise 采购成本，按渠道2的30%目标派生；响应同时返回 procurement_resource_type、allowed_channels、base_source_id、target_margin、bonus_rate。cost_evidence 明确为 frozen_procurement_basis_not_verified_actual_supplier_cost，不声称已取得真实号池扣款或保证实际利润。

## 计算和信任

原 source 必须是 quote_frozen_retail_20261019、通过原有 canonical supplier/retail-anchor/FX 证据重建，并与实际持久化父记录匹配。原成本、币种、FX、resource、报价时间和校验和不变，新源用 CashMarginBaseSourceID 引用它。

每个原始 canonical 档位和维度分别计算：采购官方价 × 对应采购折扣 × 冻结上游FX，再除冻结平台FX；售价为该采购基准 × 1.05 ÷ (1-目标毛利)。最后用精确有理数向上舍入到8位，在 TokenRules 中保存为10位字符串（末2位为0），避免配置 DECIMAL(20,8) 再舍入降低底价。BuildPricePreview 的输入/输出摘要取各档最大值；实际请求仍按原 Unicode 条件选择分档，缓存输入互斥。

PublicationGate、quotation reader、active publication reader 和 snapshot BeforeCreate 均重新验证父 source；新源成本/币种/FX/resource/tier/sale/父引用不匹配即拒绝，不能靠导入新 policy 绕过校验。没有任意成本或任意目标毛利的写入参数。

## 两阶段启用

1. 第一阶段只部署新 reader 和必要的新增列迁移。ZTAPIModelPriceSource 新增 cash_margin_base_source_id，默认0；已有 model migration 包含该 struct。代码没有启动自动改价、自动apply或自动恢复逻辑。保留新增列供回滚兼容，不删历史 source/snapshot。
2. 对父任务的 GLM53 镜像做源码/文件清单比对。共享仓库已有 GLM53/其他未提交改动必须保留，不能把整个 dirty worktree 当成这次定价增量直接覆盖生产。
3. 保存部署前后27个发布绑定、所有暂停状态以及余额/账本的基线；确认第一阶段价格和绑定完全不变。既有后台任务/迁移的运行方式由现有部署方案控制，本功能没有增加调度 worker。
4. 用下面的精确版本和快照请求预览，验收3项的原 resource、真实渠道、原价格源、canonical tiers（Flash两档、Pro两档、Kimi一档）及新售价，再显式 apply。

## API

使用现有管理员鉴权；New-Api-User 必须匹配管理员，JSON Content-Type。preview/downgrade-status 需 model.read，apply/restore 需 model.write。本文只说明调用，未执行任何生产请求。

- POST /api/models/ztapi/cash-margin-20261030/preview
- POST /api/models/ztapi/cash-margin-20261030/apply
- POST /api/models/ztapi/cash-margin-20261030/restore
- GET /api/models/ztapi/cash-margin-20261030/downgrade-status

预览或应用（ID/版本/快照必须从受控只读基线取得，不使用这里的占位符）：

```json
{
  "confirm": true,
  "models": [
    {"model_id": 61, "version": 0, "snapshot_id": 0},
    {"model_id": 64, "version": 0, "snapshot_id": 0},
    {"model_id": 86, "version": 0, "snapshot_id": 0}
  ]
}
```

以上示意 ID 也必须用实际基线替换；version/snapshot_id 为0会被拒绝。preview 不要求 confirm，支持 rollback=true 预览恢复；apply/restore 必须 confirm=true。单次允许1至3项，重复ID、范围外型号、暂停型号、资源/路由不符、源篡改、丢档、版本/快照冲突均拒绝。预览不持久化数据。

apply 成功时三项通常 imported=3、republished=3、unchanged=0，独立新 source/snapshot/audit，配置版本各加1。完全相同基线的重试通过已提交 audit 证明幂等，返回 unchanged；其他并发变化返回409。全批使用目录锁和一个事务，任一项失败整批回滚。保留原 allowed channels、verification IDs、分组和身份，不调用 verify，不写钱包或结算表，不改 published 状态。

成功后由受控只读核验记录新的 config.version/publication_snapshot_id/price_source_id 和审计；不要仅凭HTTP200或计数认定已验收。其余型号的绑定/价格、暂停状态、余额及账本必须与基线一致。

## 恢复与旧 binary 降级

不能在新价格仍生效时直接换回旧 binary。应在仍支持新 policy 的 reader 上执行以下顺序：

1. 暂停这3个型号的新请求准入，等待已准入但尚未落持久化结算记录的请求全部排空。downgrade-status 是只读观察，不是请求准入围栏，不能替代这一步。
2. 保存 apply 前的原 snapshot ID，以及当前新版本/快照；调用 restore，按每项提供 restore_snapshot_id。

```json
{
  "confirm": true,
  "models": [
    {"model_id": 61, "version": 0, "snapshot_id": 0, "restore_snapshot_id": 0}
  ]
}
```

3. restore 重验当前派生源、原 source、原 snapshot 的型号/价格/规则/路由/验收绑定。它复制原采购源形成新的 quote_frozen_retail_20261019 价格源，再形成新的恢复快照（配置版本继续加1），不改旧记录。新建旧 policy 源可确保旧 binary 的“最新价格源”管理路径也不遇到新 policy。三项恢复通常 imported=3、republished=3。
4. 验证原分档售价、原路由和验收均恢复；其余型号及历史账本/用户余额不变。
5. 在保持准入停止/排空的前提下检查 downgrade-status。存在生效新 policy、reserved/pending 等非终态请求引用新 policy 或新源、或结算快照不明确时返回409和阻断ID；数据库读取失败返回503，不能降级。
6. 只有 HTTP200 且 can_downgrade=true，并再次确认准入仍被隔离且无旧的在途捕获快照，才允许降级旧 binary。存在新 policy pending 请求时保留兼容 reader，按正常证据结算流程处理，绝不改写冻结请求价来制造“已排空”。

新 policy 的历史 source/snapshot 和终态结算记录保留。该功能不能阻止外部操作者绕过检查替换 binary；部署脚本必须把状态和准入排空作为硬前置条件。

## 本地验证命令

```powershell
go test ./model ./controller -run '^TestCashMargin20261030' -count=1
go test ./model ./controller ./router -count=1 -p 1
go test -short ./... -count=1 -p 2 -timeout 5m
```

本次执行前在子进程清空 SQL_DSN 和各显式 MySQL/Redis 集成测试配置，GOPROXY=off，避免真实外部系统连接；测试只使用隔离临时数据库/httptest，不触发模型调用。最终结果及源文件 hash 由本次交付单列，不把此前失败或未完成命令写成通过。

最终结果：13个新增顶层测试通过；完整model/controller/router回归通过（188.818 / 18.243 / 1.194秒）。全仓-short运行未全绿：model包300秒超时，观察到超时时正在执行既有TestZTAPIMediaPublicationRequiresSameVideoContractAcrossEligibleChannels，未修改该媒体测试或业务代码。其他已返回包通过。旧镜像启动校验由父任务在隔离环境执行，本任务未运行旧镜像。

最终运行源文件10项、测试文件4项的完整路径/字节数/SHA-256及精确测试结果，见docs/evidence/cash-margin-20261030-source-manifest.json。不存在依赖新只读计算器文件ztapi_cash_margin_preview.go才能部署的问题，新版本所需的有理数计算已自包含在版本化policy文件中；不要把共享仓库其他未提交文件自动纳入部署。
