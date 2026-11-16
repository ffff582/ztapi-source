package support

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestXiaoZUsesSeriousToneForFailuresAndMoney(t *testing.T) {
	require.Contains(t, supportPolicyFor("接个梗"), "小Z")
	require.Contains(t, supportPolicyFor("接个梗"), "接梗")
	for _, q := range []string{"这笔扣费不对", "Claude一直重连503", "账号不能用了"} {
		p := supportPolicyFor(q)
		require.Contains(t, p, "严肃")
		require.Contains(t, p, "不要调侃")
	}
}
func TestUnsupportedActionClaimsCannotReachWebsiteCustomer(t *testing.T) {
	for _, text := range []string{"我已经给你退款了。", "已为您切换渠道。", "我已将问题转交人工。", "已经退款了。", "已修复，请重试。", "已转人工，客服已经接管。", "Refund processed successfully.", "The issue is fixed."} {
		out := safeSupportReply(text)
		require.NotEqual(t, text, out)
		require.Contains(t, out, "没有执行")
	}
	text := "退款是否成立需要先核对证据，最终由人工处理。"
	require.Equal(t, text, safeSupportReply(text))
}
func TestPublicKnowledgeDoesNotContradictSeparatelyVerifiedPrivateEvidence(t *testing.T) {
	r := &Reader{}
	text, err := r.PublicKnowledge(context.Background(), "ClaudeCode配置")
	require.NoError(t, err)
	require.Contains(t, text, "匿名读取")
	require.Contains(t, text, "已核验")
	require.Contains(t, text, "群聊")
}
