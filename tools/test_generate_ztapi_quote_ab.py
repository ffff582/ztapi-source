import unittest
import json
from decimal import Decimal
from pathlib import Path

from tools.generate_ztapi_quote_ab import parse_token_price_rules


class TokenPriceRulesTest(unittest.TestCase):
    def test_gpt_long_context_uses_each_tier_own_prices(self):
        text = (
            "输入长度≤272K｜输入单价=$4｜缓存命中=$0.4｜缓存写入=$5｜输出=$20；"
            "输入长度>272K｜输入单价=$8｜缓存命中=$0.8｜缓存写入=$10｜输出=$30"
        )
        rules = parse_token_price_rules(text, Decimal("0.78"), Decimal("0.8"))
        self.assertEqual(2, len(rules))
        self.assertEqual("输入长度≤272K", rules[0]["conditions"][0])
        self.assertEqual("3.12", rules[0]["cost"]["input_tokens"])
        self.assertEqual("3.9", rules[0]["sale"]["input_tokens"])
        self.assertEqual("19.5", rules[0]["sale"]["output_tokens"])
        self.assertEqual("7.8", rules[1]["sale"]["input_tokens"])
        self.assertEqual("29.25", rules[1]["sale"]["output_tokens"])

    def test_claude_cache_dimensions(self):
        text = "输入单价=US$10｜缓存写入5m=US$12.5｜缓存写入1h=US$20｜缓存命中=US$1｜输出单价=US$50"
        rules = parse_token_price_rules(text, Decimal("0.65"), Decimal("0.7"))
        self.assertEqual("6.5", rules[0]["cost"]["input_tokens"])
        self.assertEqual("11.6071428571", rules[0]["sale"]["cache_write_5m"])
        self.assertEqual("46.4285714286", rules[0]["sale"]["output_tokens"])

    def test_glm_cny_and_embedding_shorthand(self):
        glm = parse_token_price_rules("输入长度≤1M｜输入单价=¥8｜输出单价=¥28｜缓存命中=¥2", Decimal("0.72"), Decimal("0.7"))
        self.assertEqual("CNY", glm[0]["currency"])
        self.assertEqual("8.2285714286", glm[0]["sale"]["input_tokens"])
        embedding = parse_token_price_rules("输入$0.02", Decimal("0.78"), Decimal("0.8"))
        self.assertEqual("0.0195", embedding[0]["sale"]["input_tokens"])
        self.assertNotIn("output_tokens", embedding[0]["sale"])

    def test_trailing_separator_and_not_applicable_cache_are_explicit(self):
        deepseek = parse_token_price_rules("计费时段=高峰｜输入单价=¥9｜输出=¥27；", Decimal("0.45"), Decimal("0.7"))
        self.assertEqual(1, len(deepseek))
        self.assertEqual("5.7857142857", deepseek[0]["sale"]["input_tokens"])
        pro = parse_token_price_rules("输入单价=$30｜缓存命中=不适用｜输出=$180", Decimal("0.78"), Decimal("0.8"))
        self.assertEqual(["cache_read"], pro[0]["not_applicable"])
        self.assertNotIn("cache_read", pro[0]["sale"])

    def test_cache_storage_is_not_silently_zero_priced(self):
        free = parse_token_price_rules("输入单价=¥8｜缓存存储=限时免费｜输出=¥28", Decimal("0.72"), Decimal("0.7"))
        self.assertEqual(["cache_storage"], free[0]["temporary_free"])
        self.assertNotIn("cache_storage", free[0]["sale"])
        for storage in ("¥0.017/百万Token/小时", "US$0.50"):
            with self.assertRaisesRegex(ValueError, "paid cache storage"):
                parse_token_price_rules(f"输入单价=$1｜缓存存储={storage}｜输出=$2", Decimal("0.82"), Decimal("0.8"))

    def test_mixed_currency_and_media_rules_do_not_silently_parse(self):
        with self.assertRaises(ValueError):
            parse_token_price_rules("输入单价=$4｜输出=¥20", Decimal("0.78"), Decimal("0.8"))
        with self.assertRaises(ValueError):
            parse_token_price_rules("输出分辨率=720p｜Token单价=$7", Decimal("0.95"), Decimal("0.8"))

    def test_all_selected_text_quote_rows_are_structurable(self):
        self.maxDiff = None
        source = Path(__file__).resolve().parent.parent / "server/model/ztapi_quotation_ab_20260915.json"
        quote = json.loads(source.read_text(encoding="ascii"))
        groups = {}
        for row in quote["entries"]:
            groups.setdefault(row["model_name"], []).append(row)
        media = {"GPT Image 2", "Gemini 2.5 Flash Image", "Seedance 2.0", "Seedance 2.0 Fast", "Seedance 2.0 Mini", "Seedance 2.5"}
        failures = []
        parsed = 0
        for name, rows in groups.items():
            if name in media:
                continue
            enterprise = [row for row in rows if row["grade"] == "A" and row["active"]]
            selected = enterprise or [row for row in rows if row["grade"] == "B" and row["active"]]
            share = Decimal("0.8") if enterprise else Decimal("0.7")
            for row in selected:
                try:
                    parse_token_price_rules(row["official_price_text"], Decimal(row["quoted_fraction"]), share)
                    parsed += 1
                except ValueError as exc:
                    failures.append(f"{name} {row['quotation_cell']}: {exc}")
        self.assertEqual(3, len(failures))
        self.assertTrue(all("paid cache storage" in item for item in failures), failures)
        self.assertGreaterEqual(parsed, 41)

    def test_generated_manifest_has_structured_selected_quote_and_blocker(self):
        source = Path(__file__).resolve().parent.parent / "server/model/ztapi_quotation_ab_20260915.json"
        rows = json.loads(source.read_text(encoding="ascii"))["entries"]
        sol = next(row for row in rows if row["model_name"] == "GPT 5.6 Sol" and row["grade"] == "A")
        self.assertEqual("text", sol["modality"])
        self.assertEqual("3.9", sol["token_price_rules"][0]["sale"]["input_tokens"])
        self.assertEqual("7.8", sol["token_price_rules"][1]["sale"]["input_tokens"])
        pro = next(row for row in rows if row["model_name"] == "Doubao Seed 2.0 Pro" and row["grade"] == "A")
        self.assertIn("paid cache storage", pro["pricing_blocker"])
        self.assertEqual([], pro["token_price_rules"])


if __name__ == "__main__":
    unittest.main()
