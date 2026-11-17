package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/shopspring/decimal"
)

type ztapiFrozenTokenPriceRule struct {
	Conditions []string          `json:"conditions"`
	Sale       map[string]string `json:"sale"`
}

var ztapiTokenLengthCondition = regexp.MustCompile(`^(输入长度|输出长度)?=?(≤|≥|>|<)([0-9]+(?:\.[0-9]+)?)(K|M)$`)

func SelectZTAPIFrozenTokenTier(snapshot *ZTAPIPublicationSnapshot, usage *dto.Usage) (*ZTAPIPublicationSnapshot, string, error) {
	if snapshot == nil || usage == nil {
		return nil, "", errors.New("token tier requires frozen price and actual usage")
	}
	selected := snapshot.Clone()
	if strings.TrimSpace(snapshot.TokenPriceRulesJSON) == "" {
		return selected, "", nil
	}
	var rules []ztapiFrozenTokenPriceRule
	if err := json.Unmarshal([]byte(snapshot.TokenPriceRulesJSON), &rules); err != nil || len(rules) == 0 {
		return nil, "", errors.New("frozen token price rules are invalid")
	}
	match := -1
	for i, rule := range rules {
		eligible, err := ztapiTokenRuleMatches(rule.Conditions, usage)
		if err != nil {
			return nil, "", err
		}
		if eligible {
			if match >= 0 {
				return nil, "", errors.New("multiple frozen token tiers match actual usage")
			}
			match = i
		}
	}
	if match < 0 {
		return nil, "", errors.New("no frozen token tier matches actual usage")
	}
	for _, name := range snapshot.BillingDimensions {
		price, err := decimal.NewFromString(rules[match].Sale[name])
		if err != nil || price.IsNegative() {
			return nil, "", fmt.Errorf("frozen token tier lacks price for %s", name)
		}
	}
	selected.SaleUSD = make(map[string]string, len(rules[match].Sale))
	for name, price := range rules[match].Sale {
		selected.SaleUSD[name] = price
	}
	return selected, fmt.Sprintf("tier_%d", match), nil
}

func ztapiTokenRuleMatches(conditions []string, usage *dto.Usage) (bool, error) {
	audio := usage.PromptTokensDetails.AudioTokens
	if audio > 0 && usage.PromptTokens > audio {
		for _, condition := range conditions {
			if strings.HasPrefix(condition, "输入类型=") {
				return false, errors.New("mixed audio and non-audio input needs separate quoted dimensions")
			}
		}
	}
	for _, condition := range conditions {
		if strings.HasPrefix(condition, "输入类型=") {
			want := strings.TrimPrefix(condition, "输入类型=")
			if (want == "音频") != (audio > 0) {
				return false, nil
			}
			if want != "音频" && want != "文本/图像/视频" {
				return false, fmt.Errorf("unsupported frozen input type %q", want)
			}
			continue
		}
		kind := ""
		for _, part := range strings.Split(condition, "且") {
			matches := ztapiTokenLengthCondition.FindStringSubmatch(part)
			if len(matches) == 0 {
				return false, fmt.Errorf("unsupported frozen token condition %q", condition)
			}
			if matches[1] != "" {
				kind = matches[1]
			}
			if kind == "" {
				return false, fmt.Errorf("frozen token condition lacks dimension %q", condition)
			}
			limit, err := strconv.ParseFloat(matches[3], 64)
			if err != nil {
				return false, err
			}
			if matches[4] == "K" {
				limit *= 1000
			} else {
				limit *= 1000000
			}
			value := float64(usage.PromptTokens)
			if kind == "输出长度" {
				value = float64(usage.CompletionTokens)
			}
			switch matches[2] {
			case "≤":
				if value > limit {
					return false, nil
				}
			case "≥":
				if value < limit {
					return false, nil
				}
			case ">":
				if value <= limit {
					return false, nil
				}
			case "<":
				if value >= limit {
					return false, nil
				}
			}
		}
	}
	return true, nil
}
