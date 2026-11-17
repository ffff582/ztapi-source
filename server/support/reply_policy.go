// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"regexp"
	"strings"
)

var seriousSupportQuestion = regexp.MustCompile(`(?i)报错|扣费|账单|余额|退款|账户|账号|故障|失败|错误|重连|断连|卡住|不能|无法|\b(?:401|403|429|500|502|503|504)\b`)
var unsupportedSupportAction = regexp.MustCompile(`(?i)(?:我|我们|客服)(?:已|已经|刚刚|正在).{0,24}(?:退款|退费|修改.{0,8}余额|调整.{0,8}余额|切换.{0,8}渠道|(?:创建|提交|转交).{0,8}(?:工单|人工))|(?:已|已经)(?:为|帮|给)(?:您|你).{0,20}(?:退款|退费|切换渠道|调整余额|修改余额)|(?:i have|we have|i've).{0,40}(?:refunded|changed your balance|switched your channel)`)
var unsupportedSupportCompletion = regexp.MustCompile(`(?i)(?:已经|已|刚刚|正在).{0,18}(?:退款|退费|退回|修复|修好|搞定|解决|转人工|转交|提交工单|创建工单|接管|上报)|(?:退款|退费|修复|工单提交|工单创建)(?:已|已经)?(?:成功|完成)|(?:人工|客服|工作人员).{0,12}(?:已|已经|正在).{0,12}(?:处理|接管|跟进)|(?:i have|we have|i've|we've).{0,32}(?:refund|fix|repair|escalat)|(?:refund|repair|handoff).{0,24}(?:complete|success)|(?:issue|problem).{0,24}(?:is fixed|has been fixed|resolved|repaired)`)

func supportPolicyFor(question string) string {
	policy := supportPolicy + "\n你的名字是小Z，是ZTAPI的AI技术助手，不冒充真人。一般交流可以接梗、偶尔轻松调侃事情，不调侃用户的技术水平或处境。配置排查按用户的客户端逐步推进，每次只问当前最必要的一个问题，不重复询问已知信息；不要求提供密码或API密钥。"
	if seriousSupportQuestion.MatchString(question) {
		return policy + "\n当前是严肃服务场景：不要调侃、接梗、使用嘲讽或卖萌语气；先说明可核实的证据和未知项，再给下一步。不要承诺扣款为零、退款、赔偿或恢复时间。"
	}
	return policy + "\n当前可以自然接梗，但不要刷屏、强行抖机灵或把猜测说成平台事实。"
}

func safeSupportReply(text string) string {
	if unsupportedSupportAction.MatchString(text) || unsupportedSupportCompletion.MatchString(text) {
		return "我没有执行退款、余额调整、渠道变更或人工转交。请先核对本人请求记录；需要人工处理时，可以提交工单并查看实际处理进度。"
	}
	return strings.TrimSpace(Redact(text))
}
