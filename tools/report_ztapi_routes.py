"""Read-only catalog audit; discovery membership never proves entitlement."""

import argparse
import csv
import json
import re
from collections import Counter
from datetime import datetime, timezone, timedelta
from pathlib import Path


def analyze(directory, repository):
    capture = json.loads((directory / "catalog-safe.json").read_text(encoding="utf-8"))
    raw = json.loads((directory / "routes-safe.json").read_text(encoding="utf-8"))
    configs = {r["id"]: r for r in raw if "snapshot_id" in r}
    channels = {r["id"]: r for r in raw if r.get("table") == "channel"}
    discovery = {r["id"]: r for r in capture["channels"]}
    recent = [r for r in raw if r.get("table") == "recent_health"]
    verifications = [r for r in raw if r.get("table") == "verification"]
    quote = json.loads((repository / "server/model/ztapi_quotation_ab_20260915.json").read_text(encoding="utf-8"))
    claims_text = (repository / "server/model/ztapi_quotation_ab_identity.go").read_text(encoding="utf-8")
    claims = {source: name for name, source in re.findall(r'"([^"\n]+)":\s*\{"([^"\n]+)",', claims_text)}
    claims.update({source: name for name, source in re.findall(r'"([^"\n]+)":\s*\{SourceModel:\s*"([^"\n]+)"', claims_text)})
    rows = []
    for model in capture["models"]:
        config = configs[model["id"]]
        source = model["source_model"]
        name = claims.get(source)
        if not name:
            candidates = {entry["model_name"] for entry in quote["entries"] if entry["model_code"].casefold() == source.casefold()}
            if len(candidates) == 1:
                name = candidates.pop()
        quoted = [e for e in quote["entries"] if e["model_name"] == name and e["active"]]
        grades = sorted({e["grade"] for e in quoted})
        allowed = json.loads(config.get("allowed_channel_ids") or "[]")
        issues = []
        routes = []
        if model["published"] and not allowed:
            issues.append("已上架但没有发布快照授权通道")
        for channel_id in allowed:
            channel = channels.get(channel_id)
            if not channel:
                issues.append(f"授权通道 {channel_id} 不存在")
                continue
            mapped = json.loads(channel.get("model_mapping") or "{}").get(source, source)
            listed = source in channel["models"].split(",")
            remote = discovery.get(channel_id)
            visible = remote and remote["discovery_success"] and mapped in (remote["discovered_ids"] or [])
            route_grade = "B" if channel["name"] == "Yunxin pool" else "A"
            if quoted and route_grade not in grades:
                issues.append(f"通道报价不匹配：通道 {channel_id} 为 {route_grade}，有效报价只有 {'/'.join(grades)}")
            if not listed:
                issues.append(f"通道 {channel_id} 的模型配置没有源模型")
            if channel["status"] != 1:
                issues.append(f"通道 {channel_id} 已停用")
            if remote and not remote['discovery_success']:
                issues.append(f"通道 {channel_id} 上游列表查询失败，不能判断目录是否包含该模型")
            elif remote and not visible:
                issues.append(f"映射后的模型 {mapped} 不在通道 {channel_id} 当前上游列表")
            if not remote:
                issues.append(f"通道 {channel_id} 上游列表未取得")
            records = [r for r in recent if r["model_id"] == model["id"] and r["channel_id"] == channel_id and r["source"] in ("real", "acceptance", "diagnostic", "probe")]
            success_at = max((r["latest_at"] for r in records if r["result"] == "success"), default=0)
            failure_at = max((r["latest_at"] for r in records if r["result"] in ("failure", "suspected")), default=0)
            passed_verifications = [v for v in verifications if v['model_id'] == model['id'] and v['channel_id'] == channel_id and v['non_streaming_passed'] and (not v['streaming_required'] or v['streaming_passed'])]
            verification_at = max((v['verified_at'] for v in passed_verifications), default=0)
            routes.append({"channel_id": channel_id, "channel_name": channel["name"], "mapped_model": mapped, "listed": listed, "discovery_visible": bool(visible), "last_success": success_at, "last_failure": failure_at})
            routes[-1]['last_verification_success'] = verification_at
        configured_channels = [c['id'] for c in channels.values() if source in c['models'].split(',')]
        if not model['published'] and model['public_name']:
            for cid in configured_channels:
                grade = 'B' if channels[cid]['name'] == 'Yunxin pool' else 'A'
                if quoted and grade not in grades:
                    issues.append(f'未上架配置仍挂在 {grade} 通道 {cid}，但当前只有 {"/".join(grades)} 报价')
        if source == 'claude-haiku-4-5-20251001':
            issues.append('待上游澄清：9.15 报价 D134 明确为 B 号池，号池 /models 也返回该型号；上游现称号池无此资源，客户最近返回 502')
        if model["public_name"] and not quoted:
            issues.append("当前 A/B 报价中未匹配到有效模型（可能已下线或尚未映射）")
        if model["published"] and routes and not any(r["last_success"] or r['last_verification_success'] for r in routes):
            issues.append("最近 30 天未找到当前授权通道成功调用或验证记录；不等于不可用")
        rows.append({"id": model["id"], "model": model["public_name"] or source, "source_model": source, "published": model["published"], "quotation_name": name, "quotation_grades": grades, "routes": routes, 'configured_channel_ids': configured_channels, "issues": issues})
    report = {"captured_at": capture["captured_at"], "config_count": len(rows), "published_count": sum(r["published"] for r in rows), "published_route_counts": dict(Counter('/'.join(str(route['channel_id']) for route in r['routes']) for r in rows if r['published'])), "models": rows}
    (directory / "audit-report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    tz = timezone(timedelta(hours=8))
    def stamp(value):
        return datetime.fromtimestamp(value, tz).strftime("%m-%d %H:%M:%S") if value else "未找到"
    with (directory / "全站模型通道审计.csv").open("w", encoding="utf-8-sig", newline="") as file:
        writer = csv.writer(file)
        writer.writerow(["模型", "上架", "报价型号", "有效报价等级", "实际授权通道", "映射后的上游模型", "最近成功（北京）", "最近验证成功（北京）", "最近失败（北京）", "问题或待确认"])
        for r in rows:
            writer.writerow([r["model"], "是" if r["published"] else "否", r["quotation_name"], '/'.join(r["quotation_grades"]), ';'.join(route["channel_name"] for route in r["routes"]), ';'.join(route["mapped_model"] for route in r["routes"]), ';'.join(stamp(route["last_success"]) for route in r["routes"]), ';'.join(stamp(route['last_verification_success']) for route in r['routes']), ';'.join(stamp(route["last_failure"]) for route in r["routes"]), ';'.join(r["issues"])])
    lines = ["# 全站模型通道审计", "", f"采集时间（UTC）：{capture['captured_at']}", f"配置 {len(rows)} 项，实际上架 {report['published_count']} 项。", "", "## 核查边界", "", "仅查询配置、授权目录、有效 A/B 报价和最近 30 天已有结果，不发送模型推理请求。调用成功列包含已有真实请求及验收记录；验证成功另列。上游 /models 返回某个 ID 不等于该密钥有该渠道的服务权限。S 报价不参与。", "", "## 上架模型", "", "| 模型 | 有效报价 | 授权通道 | 最近调用成功（北京） | 最近验证成功（北京） | 问题或待确认 |", "|---|---|---|---|---|---|"]
    for r in rows:
        if not r["published"]:
            continue
        lines.append('| '+ ' | '.join([r['model'], '/'.join(r['quotation_grades']), '; '.join(route['channel_name'] for route in r['routes']), '; '.join(stamp(route['last_success']) for route in r['routes']), '; '.join(stamp(route['last_verification_success']) for route in r['routes']), '; '.join(r['issues']) or '静态配置未发现矛盾；不是当前连通性保证'])+' |')
    lines += ["", "## 未上架但有公开型号的配置", ""]
    for r in rows:
        if not r['published'] and r['model'].startswith('zt-'):
            lines.append(f"- {r['model']}：{'；'.join(r['issues']) or '目前未上架，本次未发起复测'}")
    (directory / "全站模型通道审计.md").write_text('\n'.join(lines)+'\n', encoding='utf-8')
    print(json.dumps({k:v for k,v in report.items() if k != 'models'}, ensure_ascii=False))
    for row in rows:
        if row['issues'] and (row['published'] or row['model'] == 'zt-claude-haiku-4.5'):
            print(row['model']+': '+'; '.join(row['issues']))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("directory", type=Path)
    parser.add_argument("--repository", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    analyze(args.directory, args.repository)
