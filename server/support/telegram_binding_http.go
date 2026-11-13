// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import "net/http"

func (h *handler) serveTelegramBinding(w http.ResponseWriter, r *http.Request, user Identity) {
	if h.cfg.TelegramBridge == nil {
		if r.Method == http.MethodGet {
			send(w, 200, BindingView{})
			return
		}
		fail(w, 503, "telegram_binding_unavailable")
		return
	}
	var view BindingView
	var err error
	switch r.Method {
	case http.MethodGet:
		view, err = h.cfg.TelegramBridge.Binding(r.Context(), user.ID)
	case http.MethodPost:
		var in BindingInput
		if !decode(w, r, &in) {
			return
		}
		view, err = h.cfg.TelegramBridge.UpdateBinding(r.Context(), user.ID, in)
	default:
		fail(w, 405, "support_method_not_allowed")
		return
	}
	if err != nil {
		storeFailure(w, err)
		return
	}
	send(w, 200, view)
}
