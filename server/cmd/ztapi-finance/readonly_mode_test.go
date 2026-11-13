package main

import (
 "net/http"
 "net/http/httptest"
 "testing"
 "github.com/stretchr/testify/require"
)

func TestFinanceRollbackModePreservesReadsAndRejectsWrites(t *testing.T) {
 calls := 0
 h := financeReadOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) }))
 for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
  w := httptest.NewRecorder(); h.ServeHTTP(w, httptest.NewRequest(method,"/api/admin/finance-reporting/report",nil)); require.Equal(t,200,w.Code)
 }
 require.Equal(t,3,calls)
 for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
  w := httptest.NewRecorder(); h.ServeHTTP(w,httptest.NewRequest(method,"/api/admin/finance-reporting/facts",nil)); require.Equal(t,http.StatusLocked,w.Code)
 }
 require.Equal(t,3,calls,"rollback mode must not invoke a write handler")
}
