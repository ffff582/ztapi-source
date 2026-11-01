#!/usr/bin/env python3
"""Two explicitly reserved support acceptance calls, never customer model calls."""
import base64
from contextlib import closing
from decimal import Decimal
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import sys
import time

import release as r


def main():
    build, baseline = r.validate_payload()
    r.check_baseline(baseline)
    r.require(json.loads((r.STAGE / 'prepared.json').read_text())['prepared'], 'candidate_not_prepared')
    budget = json.loads((r.STAGE / 'budget-proof.json').read_text())
    r.require(budget['reserved_u'] == '0.5' and budget['total_cap_u'] == '15' and Decimal(budget['total_occupied_u']) <= Decimal('15') and len(budget['attempt_ids']) == 2, 'global_test_budget_not_reserved')
    r.require(not (r.STAGE / 'acceptance-budget.json').exists(), 'existing_acceptance_budget_requires_receipt_reconciliation')
    r.json_save('acceptance-budget.json', {'total_cap_u': '0.5', 'reserved_u': '0.5', 'reserved_calls': 2, 'per_call_reserved_u': '0.25', 'release': build['commit'], 'automatic_retries': False})
    key = r.envmap(r.inspect(r.MAIN))['ZTAPI_SESSION_SIGNING_KEY']
    base = r.address(r.SUPPORT, 3012)
    def call(method_path, data=None):
        status, value = r.fetch(base + method_path, key, data=data)
        r.require(status in [200, 201] and isinstance(value, dict) and value.get('success'), 'acceptance_http_failed:' + str(status))
        return value['data']
    rows = []
    conversations = []
    fixtures = [
        ('text', '我使用 Claude Code 接入 ZTAPI，Base URL 应该填什么？哪些情况需要转人工？请用两句话回答，不要要求我提供密钥。', None),
        ('vision', '只根据截图指出 HTTP 状态码，并给一条无需密钥的排查建议。不要猜测我的余额或承诺退款。', {'mime_type': 'image/png', 'data_base64': base64.b64encode((r.STAGE / 'vision-fixture.png').read_bytes()).decode()}),
    ]
    for kind, text, image in fixtures:
        conversation = call('/api/support/conversations', {'idempotency_key': 'support-live-20261102-r1-' + kind})
        conversations.append(conversation['id'])
        data = {'content': text, 'idempotency_key': 'support-live-message-20261102-r1-' + kind}
        if image:
            data['image'] = image
        started = time.monotonic()
        reply = call('/api/support/conversations/' + conversation['id'] + '/messages', data)
        elapsed = int((time.monotonic() - started) * 1000)
        answer = reply['reply']['content']
        r.require(reply['reply']['role'] == 'assistant' and bool(answer.strip()), 'support_live_not_completed:' + kind)
        r.require(('https://ztapi.vip' in answer) if kind == 'text' else ('503' in answer), 'support_live_answer_failed:' + kind)
        with closing(sqlite3.connect('file:' + str(r.ROOT / 'data/support.db') + '?mode=ro', uri=True)) as db:
            db.row_factory = sqlite3.Row
            receipt = dict(db.execute('SELECT c.id,c.dispatched,r.outcome,r.input_tokens,r.output_tokens,r.cache_read_tokens,r.quote_cost_u,r.upstream_credit,r.actual_cost_u,r.upstream_request_id,r.quote_evidence FROM support_calls c JOIN support_call_receipts r ON r.call_id=c.id WHERE c.id=?', (reply['call_id'],)).fetchone())
        r.require(receipt['dispatched'] == 1 and receipt['outcome'] == 'completed' and receipt['quote_cost_u'] is not None and Decimal(receipt['quote_cost_u']) <= Decimal('0.25'), 'support_live_receipt_failed:' + kind)
        r.require(receipt['actual_cost_u'] is None, 'quote_mislabeled_as_actual')
        rows.append({'kind': kind, 'conversation_id': conversation['id'], 'receipt': receipt, 'elapsed_ms': elapsed, 'answer': answer})
        r.json_save('acceptance-' + kind + '.json', rows[-1])
    # Human handoff must not dispatch an extra model request.
    cid = conversations[0]
    ticket = call('/api/support/tickets', {'conversation_id': cid, 'subject': '[管理验收] 人工接管', 'content': '仅验收客服链路，不修改余额或渠道。', 'idempotency_key': 'support-live-ticket-20261102-r1'})
    followup = call('/api/support/conversations/' + cid + '/messages', {'content': '补充验收信息，请人工回复。', 'idempotency_key': 'support-live-human-followup-20261102-r1'})
    r.require(not followup.get('call_id'), 'human_followup_dispatched')
    human = call('/api/admin/support/tickets/' + str(ticket['id']) + '/replies', {'content': '人工链路验收完成，未调整账户余额、退款或渠道。', 'idempotency_key': 'support-live-staff-reply-20261102-r1'})
    detail = call('/api/support/conversations/' + cid)
    r.require(any(x['role'] == 'human' for x in detail['messages']), 'human_reply_not_visible')
    current = detail['ticket']
    call('/api/admin/support/tickets/' + str(ticket['id']) + '/status', {'status': 'resolved', 'version': current['version'], 'idempotency_key': 'support-live-resolve-20261102-r1'})
    with closing(sqlite3.connect('file:' + str(r.ROOT / 'data/support.db') + '?mode=ro', uri=True)) as db:
        count = db.execute('SELECT COUNT(*) FROM support_calls').fetchone()[0]
    r.require(count == 2, 'acceptance_created_unreserved_calls')
    # The finance cutoff is quantized by 15 seconds; wait without another model call.
    finance_report = None
    for _ in range(18):
        status, result = r.fetch(r.address(r.FINANCE, 3011) + '/api/admin/finance-reporting/report?month=2026-11', key)
        if status == 200 and result['data']['support_source_status'] == 'available' and len(result['data']['customer_service_costs']) == 2:
            finance_report = result['data']
            break
        time.sleep(2)
    r.require(finance_report is not None, 'automatic_finance_read_failed')
    quoted = sum(Decimal(row['receipt']['quote_cost_u']) for row in rows)
    projected = sum(Decimal(row['net_usdt']) for row in finance_report['customer_service_costs'])
    r.require(abs(quoted - projected) < Decimal('0.000002') and quoted <= Decimal('0.5'), 'support_finance_amount_mismatch')
    r.check_baseline(baseline)
    result = {'passed': True, 'paid_calls': 2, 'paid_cap_u': '0.5', 'reserved_u': '0.5', 'quoted_cash_u': str(quoted), 'actual_cash_u': None, 'cost_basis': 'quote_not_supplier_debit', 'finance_automatic_rows': 2, 'human_followup_no_model_call': True, 'main_api_unchanged': True, 'text_and_vision_passed': True, 'call_ids': [row['receipt']['id'] for row in rows]}
    r.json_save('acceptance.json', result)
    print(json.dumps(result))


if __name__ == '__main__':
    os.umask(0o077)
    try:
        main()
    except Exception as e:
        print(json.dumps({'passed': False, 'error_type': type(e).__name__, 'reason': str(e) if isinstance(e, RuntimeError) else 'acceptance_operation_failed', 'automatic_retry': False}))
        raise SystemExit(1)
