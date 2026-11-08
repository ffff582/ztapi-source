# 图片按张收费与4K交付修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按负责人2026-11-09确认的新张价上线GPT Image 2，修复可复现交付问题，客户费用与上游Token成本分别记账。

**Architecture:** 保留原采购Token矩阵与历史冻结快照，新增独立的不可变按张销售合同，只在新发布快照上启用。请求派发前计算并冻结张价及扩展费，完成后按同一冻结合同结算，不再加收Token费；原Token回执独立保留用于成本财务。

**Tech Stack:** Go/GORM/Decimal、React/TypeScript/Vitest、现有金融CI与候选平滑发布工具。

**Spec:** 负责人本线程确认：1K/2K每张0.30U，4K每张0.60U；含2000本地估算Token及1份标准参考图档，每额外图档+0.05U，每额外不足或满2000Token+0.02U，参考图按2048x2048像素面积向上取整；总预算10U，不改历史账或客户余额。

## 全局约束

- 不重放、不重算、不补退历史客户请求；不手工修改余额或客户Key。
- 不修改供应商渠道与模型映射，不启用负余额方案。
- 本轮已有4次真实上游调用，报价估算0.78960373U，未知实扣预留9.14416403U继续占用；剩余0.85583597U不是可无条件发出4K请求的预算。
- 不为测试扩大预算，不把估算改标实际扣款；优先断网与假上游复现，真实调用预留不足就停止。
- 每张总价=(分辨率基础价+max(参考图档-1,0)*0.05+ceil(max(提示词Token-2000,0)/2000)*0.02)。总预扣=每张总价*请求张数。
- 原API请求参数与现有张数范围保留，不把4000字符限制重新引入。
- 扩展费属于固定图片价格，不在结算后按实际Token补收。
- 供应商报价与统计用量不是成本硬封顶保证，公开文案不得承诺每单绝无亏损。

## Review Focus

1. 旧请求跨发布结算仍按旧冻结合同，不受新张价影响。
2. 参考图头部伪造、极大尺寸、面积乘法与金额溢出不能绕过计价或触发内网抓取。
3. 请求重放、缺少用量、上游用量异常与下游中断不能触发重复扣费或自动退款。
4. 并行新预扣与余额检查仍为一个数据库事务；新张价不会通过差额补扣透支。
5. 目录、调用记录、财务、实际账本的销售单位一致；Token仅用于采购成本，不重复计入销售。

## Task 1: 销售合同与精确定价

Files: 新增`server/types/ztapi_image_sale.go`及测试；新增`server/service/ztapi_image_sale.go`及测试。

Interface: `types.ZTAPIImageSaleContract`与`ZTAPIImageSaleQuote`；合同严格解析并规范化，报价使用Decimal和整数档位；图片计价只读已上传文件头，不增加远程URL读取。

- [ ] RED：0/2000/2001/4000/4001提示词边界、0/1/2图档、分辨率、n、重复字段、非法/超大数值。
- [ ] GREEN：基础0.30/0.60、扩展0.05/0.02、面积向上取整，完整冻结报价。

## Task 2: 新请求预扣与幂等结算

Files: `server/relay/common/relay_info.go`、`server/controller/relay.go`、`server/service/ztapi_durable_billing.go`、`server/service/ztapi_media_billing.go`及新增按张结算测试。

- [ ] RED：恰好张价余额可用、不足拒绝；成功只收冻结张价；供应商Token高于旧估值不追加客户费；重复响应不重复扣；旧快照金额不变。
- [ ] GREEN：派发前归一化并冻结报价。已验证图片结果与供应商用量独立处理；缺失Token回执标记成本待核对，不把已确认张费与未知采购费用混淆。

## Task 3: 不可变发布与公共目录

Files: `server/model/ztapi_model_evidence.go`、新建按张发布model/controller文件、`server/model/ztapi_public_catalog.go`、`server/router/api-router.go`、`server/relay/common/ztapi_publication_snapshot.go`或对应现有快照定义文件。

- [ ] RED：仅模型85、原价源1873、原渠道[1]、版本冲突与重复提交；禁止调用方偷改单价；历史快照不变。
- [ ] GREEN：快照增加可选`image_sale_contract_json`，公开只暴露销售结构`image_sale`，不输出采购源/渠道/凭据。旧Token合同保留成本用途。
- [ ] 推广成本门禁同步新目录指纹及新销售单位，不能拿原Token收入冒充按张收入；按报价边界与额外档位核算，不伪称实际实扣。

## Task 4: 4K交付保护

Files: 独立工作单负责`server/service/http.go`、`server/relay/channel/openai/relay_image.go`及交付测试。

- [ ] RED：大响应、响应头、短写、断开、flush错误；保留原始失败证据，不伪称已定位网络层唯一原因。
- [ ] GREEN：最小修复与足够的写入结果记录；HTTP已写后不拼接JSON错误、不重发收费请求、不将客户断开视为上游零扣费。

## Task 5: 前端和财务

Files: 独立前端工作单负责目录contracts与models组件；主线负责`server/finance/settlement_usage*.go`或对应现有用量读取实现、日志元数据和财务回归测试。

- [ ] RED：旧模型兼容；新模型显示U/张和元/张、基础/扩展档，隐藏旧Token折扣；财务Token不与图片销售单位混算。
- [ ] GREEN：公共与登录目录一致；测试费用独立归集，客户张费、供应商估算与实际账单分层。

## Task 6: 验收与发布

- [ ] 冻结只包含本任务变更的精确提交，保留用户其他dirty改动与index。
- [ ] Go计费/发布/财务回归、离线4K交付、前端相关与完整回归、Typescript/lint/build。
- [ ] 同一提交金融CI通过并推送公开production标签，独立候选真实API与大图假上游完整传输验证。
- [ ] 备份和回滚路径核验；排空后平滑切换，不重建MySQL/Redis/Nginx。
- [ ] 通过正式发布入口启用新张价，更新推广凭证，核对目录与账本，历史账单/余额未人工修改。

## 执行记录

- 2026-11-09：价格与扩展档位已获负责人确认；旧欠费草案已撤回。当前只读基线与预算证据已核对。
- Task 1: completed，本地合同/档位/像素面积测试通过
- Task 2: completed，本地预扣/结算/重放/缺失及超大Token回执/实际派发字节绑定测试通过
- Task 3: completed，本地版本冲突、不可变发布、恢复legacy、再次发布继承与health下架策略保留测试通过
- Task 4: in progress，短写/断开/多层包装flush错误与22MB完整交付离线验证通过；交付失败保留预扣并待核对，不记成功；尚未证明历史4K唯一根因或通过修复后真实4K验收
- Task 5: completed，本地前端180项与12个浏览器场景、财务Token成本/按张销售分离及报价返利门禁测试通过
- Task 6: pending

发布预算门槛：4K修复后的真实复验尚未通过。现有未知实扣预留9.14416403U继续占用，剩余0.85583597U不足以覆盖一次4K原合同全额报价预留3.86082993U；不得据0.78960373U估算释放差额。先完成冻结提交及零费用验收，实扣凭证核销前不启用生产张价。
