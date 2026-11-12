package finance

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFinanceSQLiteProcessWorker(t *testing.T) {
	path := os.Getenv("FINANCE_PROCESS_DB")
	if path == "" {
		return
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	s, err := NewStore(db)
	require.NoError(t, err)
	fmt.Println("WORKER_READY")
	_, err = bufio.NewReader(os.Stdin).ReadByte()
	require.NoError(t, err)
	now := moment("2026-10-30T00:00:00Z")
	key := os.Getenv("FINANCE_PROCESS_KEY")
	var in Submission
	kind := os.Getenv("FINANCE_PROCESS_KIND")
	if kind == "direct" || kind == "mixed" && key == "worker-1" {
		in = Submission{Kind: "direct_cost", DirectCost: &DirectCost{Supplier: "Xcloud", Account: "account", BillID: "shared-bill", VerificationID: 1, ChannelID: 2, OccurredAt: moment("2026-10-29T00:00:00Z"), Amount: "1", Currency: "XCLOUD_CREDIT", Evidence: "receipt", LinkEvidence: "verification link", Coverage: "unknown"}, IdempotencyKey: key}
	} else if kind == "test" || kind == "mixed" {
		in = requestBill(t, key, "SHARED-BILL")
		in.IdempotencyKey = key
	} else {
		channel := 2
		if key == "worker-2" {
			channel = 3
		}
		in = Submission{Kind: "cost", Ref: fmt.Sprintf("2026-09:%d", channel), Cost: &Cost{Month: "2026-09", ChannelID: channel, Supplier: "Xcloud", Gross: "1", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "same monthly invoice"}, IdempotencyKey: key}
	}
	_, err = s.Append(context.Background(), in, 1, now)
	if err == nil {
		fmt.Println("WORKER_SUCCESS")
		return
	}
	message := strings.ToLower(err.Error())
	if err == ErrConflict || strings.Contains(message, "locked") || strings.Contains(message, "busy") {
		fmt.Println("WORKER_REJECTED")
		return
	}
	t.Fatalf("unexpected append error: %v", err)
}

func TestSQLiteSeparateProcessesCannotDuplicateBills(t *testing.T) {
	for _, kind := range []string{"direct", "test", "mixed", "monthly"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "finance.db")
			db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			require.NoError(t, err)
			s, err := NewStore(db)
			require.NoError(t, err)
			var workers []*exec.Cmd
			var inputs []interface {
				Write([]byte) (int, error)
				Close() error
			}
			var outputs []*bufio.Scanner
			var stderr [2]bytes.Buffer
			for i := 0; i < 2; i++ {
				cmd := exec.Command(os.Args[0], "-test.run=^TestFinanceSQLiteProcessWorker$")
				cmd.Env = append(os.Environ(), "FINANCE_PROCESS_DB="+path, "FINANCE_PROCESS_KIND="+kind, fmt.Sprintf("FINANCE_PROCESS_KEY=worker-%d", i+1))
				cmd.Stderr = &stderr[i]
				stdin, e := cmd.StdinPipe()
				require.NoError(t, e)
				stdout, e := cmd.StdoutPipe()
				require.NoError(t, e)
				require.NoError(t, cmd.Start())
				workers = append(workers, cmd)
				inputs = append(inputs, stdin)
				outputs = append(outputs, bufio.NewScanner(stdout))
			}
			for _, out := range outputs {
				ready := false
				for out.Scan() {
					if out.Text() == "WORKER_READY" {
						ready = true
						break
					}
				}
				require.True(t, ready, "worker did not initialize")
			}
			for _, in := range inputs {
				_, err = in.Write([]byte{'x'})
				require.NoError(t, err)
				require.NoError(t, in.Close())
			}
			success, rejected := 0, 0
			for i, out := range outputs {
				for out.Scan() {
					if out.Text() == "WORKER_SUCCESS" {
						success++
					}
					if out.Text() == "WORKER_REJECTED" {
						rejected++
					}
				}
				require.NoError(t, workers[i].Wait(), stderr[i].String())
			}
			require.Equal(t, 1, success)
			require.Equal(t, 1, rejected)
			facts, err := s.List(context.Background())
			require.NoError(t, err)
			require.Len(t, facts, 1)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())
		})
	}
}
