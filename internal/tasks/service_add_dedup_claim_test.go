package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/rpc"
	surgetypes "goaria-v3/internal/surge/types"
)

func TestServiceAddDedup_TwoServicesCoalesceAddUri(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svcA := &Service{Engine: engine, Deduper: d}
	svcB := &Service{Engine: engine, Deduper: d}
	url := "http://127.0.0.1/files/coalesce.bin"

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		resA := make(chan string, 1)
		go func() { resA <- svcA.AddUri(url) }()
		<-engine.entered // owner is inside the engine

		resB := make(chan string, 1)
		go func() { resB <- svcB.AddUri(url) }()
		synctest.Wait() // loser is attached and durably blocked

		if got := engine.addCount(); got != 1 {
			t.Fatalf("engine adds = %d, want 1 while loser waits", got)
		}
		select {
		case r := <-resB:
			t.Fatalf("loser returned before owner settled: %q", r)
		default:
		}

		engine.release()
		if got := <-resA; got != "success" {
			t.Fatalf("owner AddUri = %q, want success", got)
		}
		if got := <-resB; got != "duplicate" {
			t.Fatalf("loser AddUri = %q, want duplicate", got)
		}
	})
}

func TestServiceAddDedup_AddUriAndExtensionCrossEntry(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svcUI := &Service{Engine: engine, Deduper: d}
	svcExt := &Service{Engine: engine, Deduper: d}

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		// Order 1: the extension request owns the claim, AddUri is loser.
		url1 := "http://127.0.0.1/files/ext-first.bin"
		resExt := make(chan extAddResult, 1)
		callExtensionAdd(svcExt, extension.DownloadRequest{URL: url1}, resExt)
		<-engine.entered

		resUI := make(chan string, 1)
		go func() { resUI <- svcUI.AddUri(url1) }()
		synctest.Wait()
		if got := engine.addCount(); got != 1 {
			t.Fatalf("engine adds = %d, want 1", got)
		}
		engine.release()
		if got := <-resExt; got.err != nil || got.gid != "gid-1" {
			t.Fatalf("extension owner = %+v, want gid-1 success", got)
		}
		if got := <-resUI; got != "duplicate" {
			t.Fatalf("AddUri loser = %q, want duplicate", got)
		}

		// Order 2: AddUri owns the claim; the extension loser gets the
		// winner's gid with its duplicate error.
		engine.resetGate()
		url2 := "http://127.0.0.1/files/ui-first.bin"
		go func() { resUI <- svcUI.AddUri(url2) }()
		<-engine.entered

		callExtensionAdd(svcExt, extension.DownloadRequest{URL: url2}, resExt)
		synctest.Wait()
		if got := engine.addCount(); got != 2 {
			t.Fatalf("engine adds = %d, want 2", got)
		}
		engine.release()
		if got := <-resUI; got != "success" {
			t.Fatalf("AddUri owner = %q, want success", got)
		}
		got := <-resExt
		if got.err == nil || got.err.Error() != "duplicate" || got.gid != "gid-2" {
			t.Fatalf("extension loser = %+v, want duplicate with winner gid-2", got)
		}
	})
}

func TestServiceAddDedup_ExtensionCoalesceAndKeyBoundaries(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svcA := &Service{Engine: engine, Deduper: d}
	svcB := &Service{Engine: engine, Deduper: d}

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		res := make(chan extAddResult, 4)

		// Same URL, two services: one engine add, loser gets the gid.
		url1 := "http://127.0.0.1/files/ext-ext.bin"
		callExtensionAdd(svcA, extension.DownloadRequest{URL: url1}, res)
		<-engine.entered
		callExtensionAdd(svcB, extension.DownloadRequest{URL: url1}, res)
		synctest.Wait()
		if got := engine.addCount(); got != 1 {
			t.Fatalf("engine adds = %d, want 1", got)
		}
		engine.release()
		first, second := <-res, <-res
		if first.err != nil {
			first, second = second, first
		}
		if first.err != nil || first.gid != "gid-1" {
			t.Fatalf("extension owner = %+v, want gid-1 success", first)
		}
		if second.err == nil || second.err.Error() != "duplicate" || second.gid != "gid-1" {
			t.Fatalf("extension loser = %+v, want duplicate gid-1", second)
		}

		// Same non-empty DedupKey, different URLs still coalesce.
		engine.resetGate()
		callExtensionAdd(svcA, extension.DownloadRequest{URL: "http://127.0.0.1/files/k-a.bin", DedupKey: "shared-key"}, res)
		<-engine.entered
		callExtensionAdd(svcB, extension.DownloadRequest{URL: "http://127.0.0.1/files/k-b.bin", DedupKey: "shared-key"}, res)
		synctest.Wait()
		if got := engine.addCount(); got != 2 {
			t.Fatalf("engine adds = %d, want 2 total", got)
		}
		engine.release()
		a, b := <-res, <-res
		if (a.err == nil) == (b.err == nil) {
			t.Fatalf("DedupKey pair must split owner/loser, got %+v %+v", a, b)
		}

		// Key-space boundaries need no gate: distinct dedup targets never
		// merge (the released gate no longer blocks adds).
		urlSame := "http://127.0.0.1/files/same.bin"
		if _, err := svcA.AddUriFromExtension(extension.DownloadRequest{URL: urlSame, DedupKey: "key-1"}); err != nil {
			t.Fatalf("key-1 add err = %v", err)
		}
		if _, err := svcB.AddUriFromExtension(extension.DownloadRequest{URL: urlSame, DedupKey: "key-2"}); err != nil {
			t.Fatalf("key-2 add err = %v, want success (different dedup target)", err)
		}
		if _, err := svcB.AddUriFromExtension(extension.DownloadRequest{URL: urlSame}); err != nil {
			t.Fatalf("url-keyed add err = %v, want success (DedupKey does not fall back)", err)
		}
		if got := engine.addCount(); got != 5 {
			t.Fatalf("engine adds = %d, want 5: coalesced pairs each add once, distinct keys add separately", got)
		}
	})
}

type dedupOutcome struct {
	succeeded  []string
	duplicates []string
	errors     map[string]string
	groups     int
	err        error
}

func preparedOutcome(res PreparedAddResult, err error) dedupOutcome {
	return dedupOutcome{succeeded: res.Succeeded, duplicates: res.Duplicates, errors: res.Errors, groups: len(res.Groups), err: err}
}

func TestServiceAddDedup_BatchEntriesCoalesceAcrossCalls(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svcOwner := &Service{Engine: engine, Deduper: d}
	svcBatch := &Service{Engine: engine, Deduper: d}
	svcDirect := &Service{Engine: engine, Deduper: d}
	svcExtractor := &Service{Engine: engine, Deduper: d}

	cases := []struct {
		name       string
		call       func(ctx context.Context, url, key string) dedupOutcome
		wantDupKey func(url, key string) string
	}{
		{
			name: "batch_add_uri",
			call: func(ctx context.Context, url, key string) dedupOutcome {
				res := svcBatch.BatchAddUri([]string{url})
				return dedupOutcome{succeeded: res.Succeeded, duplicates: res.Duplicates, errors: res.Errors, groups: len(res.Groups)}
			},
			wantDupKey: func(url, key string) string { return url },
		},
		{
			name: "prepared_direct",
			call: func(ctx context.Context, url, key string) dedupOutcome {
				res, err := svcDirect.AddPreparedDirectItems(ctx, PreparedDirectAddRequest{
					Items: []PreparedDirectAddItem{{ClientItemID: key, URL: url}},
				})
				return preparedOutcome(res, err)
			},
			wantDupKey: func(url, key string) string { return key },
		},
		{
			name: "prepared_extractor",
			call: func(ctx context.Context, url, key string) dedupOutcome {
				res, err := svcExtractor.AddPreparedExtractorItems(ctx, PreparedAddRequest{
					Items: []PreparedAddItem{{Item: ResolvedItem{URL: url}, DisplayKey: key}},
				})
				return preparedOutcome(res, err)
			},
			wantDupKey: func(url, key string) string { return key },
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := fmt.Sprintf("http://127.0.0.1/files/entry-%d.bin", i)
			key := fmt.Sprintf("dupkey-%d", i)

			synctest.Test(t, func(t *testing.T) {
				engine.entered = make(chan struct{}, 16)
				engine.resetGate()
				resOwner := make(chan string, 1)
				go func() { resOwner <- svcOwner.AddUri(url) }()
				<-engine.entered

				out := make(chan dedupOutcome, 1)
				go func() { out <- tc.call(context.Background(), url, key) }()
				synctest.Wait()

				engine.release()
				if got := <-resOwner; got != "success" {
					t.Fatalf("owner = %q, want success", got)
				}
				res := <-out
				if res.err != nil {
					t.Fatalf("loser call err = %v", res.err)
				}
				wantDup := tc.wantDupKey(url, key)
				if len(res.duplicates) != 1 || res.duplicates[0] != wantDup {
					t.Fatalf("loser duplicates = %v, want [%q]", res.duplicates, wantDup)
				}
				if len(res.succeeded) != 0 || len(res.errors) != 0 || res.groups != 0 {
					t.Fatalf("loser must not record success/error/group: %+v", res)
				}
			})
		})
	}
}

func TestServiceAddDedup_LoserGroupAccountingUntouched(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svcOwner := &Service{Engine: engine, Deduper: d}
	svcMix := &Service{Engine: engine, Deduper: d}
	shared := "http://127.0.0.1/files/shared-group.bin"
	own := "http://127.0.0.1/files/own-group.bin"

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 16)
		engine.resetGate()
		resOwner := make(chan string, 1)
		go func() { resOwner <- svcOwner.AddUri(shared) }()
		<-engine.entered

		out := make(chan PreparedAddResult, 1)
		go func() {
			res, _ := svcMix.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{
				CreateGroup: true,
				Items: []PreparedAddItem{
					{Item: ResolvedItem{URL: shared}, DisplayKey: "lose-key"},
					{Item: ResolvedItem{URL: own}, DisplayKey: "win-key"},
				},
			})
			out <- res
		}()
		<-engine.entered // the call's own winner item reached the engine
		synctest.Wait()
		if got := engine.addCount(); got != 2 {
			t.Fatalf("engine adds = %d, want 2 (shared+own)", got)
		}
		engine.release()

		if got := <-resOwner; got != "success" {
			t.Fatalf("owner = %q, want success", got)
		}
		res := <-out
		if len(res.Duplicates) != 1 || res.Duplicates[0] != "lose-key" {
			t.Fatalf("Duplicates = %v, want [lose-key]", res.Duplicates)
		}
		if len(res.Succeeded) != 1 || res.Succeeded[0] != "win-key" {
			t.Fatalf("Succeeded = %v, want [win-key]", res.Succeeded)
		}
		if len(res.Errors) != 0 {
			t.Fatalf("Errors = %v, want empty", res.Errors)
		}
		// The group belongs to the real winner item; the loser neither
		// duplicates it nor registers a second group.
		if len(res.Groups) != 1 {
			t.Fatalf("Groups = %d, want exactly the winner's group", len(res.Groups))
		}
	})
}

func TestServiceAddDedup_ResolvedCandidateKeyCoalesces(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	shared := "http://127.0.0.1/files/resolved.bin"
	adA := &refRecordingAdapter{fakePortAdapter: fakePortAdapter{items: []ResolvedItem{
		{URL: shared, Ref: "ref-a", Filename: "a.bin"},
	}}}
	adB := &refRecordingAdapter{fakePortAdapter: fakePortAdapter{items: []ResolvedItem{
		{URL: shared, Ref: "ref-b", Filename: "b.bin"},
	}}}
	svcA := &Service{Engine: engine, Adapter: adA, Deduper: d}
	svcB := &Service{Engine: engine, Adapter: adB, Deduper: d}

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		resA := make(chan string, 1)
		go func() { resA <- svcA.AddUri("http://127.0.0.1/source/page-a") }()
		<-engine.entered

		resB := make(chan string, 1)
		go func() { resB <- svcB.AddUri("http://127.0.0.1/source/page-b") }()
		synctest.Wait()
		if got := engine.addCount(); got != 1 {
			t.Fatalf("engine adds = %d, want 1", got)
		}
		engine.release()
		if got := <-resA; got != "success" {
			t.Fatalf("owner = %q, want success", got)
		}
		if got := <-resB; got != "duplicate" {
			t.Fatalf("loser = %q, want duplicate", got)
		}

		// Each side released its own neutral ref — the loser never borrowed
		// the owner's settlement.
		if got := adA.releasedSnapshot(); len(got) != 1 || got[0] != "ref-a" {
			t.Fatalf("adapter A releases = %v, want [ref-a]", got)
		}
		if got := adB.releasedSnapshot(); len(got) != 1 || got[0] != "ref-b" {
			t.Fatalf("adapter B releases = %v, want [ref-b]", got)
		}

		// Distinct resolved URLs never merge, even off one source URL.
		engine.disarm()
		adC := &refRecordingAdapter{fakePortAdapter: fakePortAdapter{items: []ResolvedItem{
			{URL: "http://127.0.0.1/files/multi-1.bin", Ref: "r1"},
			{URL: "http://127.0.0.1/files/multi-2.bin", Ref: "r2"},
		}}}
		svcC := &Service{Engine: engine, Adapter: adC, Deduper: d}
		if got := svcC.AddUri("http://127.0.0.1/source/page-c"); got != "success" {
			t.Fatalf("multi-item AddUri = %q, want success", got)
		}
		if got := engine.addCount(); got != 3 {
			t.Fatalf("engine adds = %d, want 3 (two distinct urls added)", got)
		}
	})
}

func TestServiceAddDedup_OwnerFailureThenRetrySucceeds(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	engine.failErr = errors.New("engine rejected add")
	svcA := &Service{Engine: engine, Deduper: d}
	svcB := &Service{Engine: engine, Deduper: d}
	url := "http://127.0.0.1/files/retry.bin"

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		resA := make(chan string, 1)
		go func() { resA <- svcA.AddUri(url) }()
		<-engine.entered

		resB := make(chan string, 1)
		go func() { resB <- svcB.AddUri(url) }()
		synctest.Wait()

		engine.failOne.Store(true)
		engine.release()
		gotA, gotB := <-resA, <-resB
		if gotA == "success" || gotA == "duplicate" {
			t.Fatalf("owner result = %q, want failure", gotA)
		}
		if gotB != gotA {
			t.Fatalf("loser result = %q, want same failure as owner %q", gotB, gotA)
		}

		// The failed claim released the key: a third submit retries for real.
		engine.disarm()
		if got := svcA.AddUri(url); got != "success" {
			t.Fatalf("retry AddUri = %q, want success", got)
		}
		if got := engine.addCount(); got != 2 {
			t.Fatalf("engine adds = %d, want 2 total", got)
		}
	})
}

func TestServiceAddDedup_SameBatchLoserFailureAccounting(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	engine.failErr = errors.New("engine rejected add")
	svc := &Service{Engine: engine, Deduper: d}
	url := "http://127.0.0.1/files/same-batch.bin"
	req := PreparedAddRequest{Items: []PreparedAddItem{
		{Item: ResolvedItem{URL: url}, DisplayKey: "key-a"},
		{Item: ResolvedItem{URL: url}, DisplayKey: "key-b"},
	}}

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		out := make(chan PreparedAddResult, 1)
		go func() {
			res, _ := svc.AddPreparedExtractorItems(context.Background(), req)
			out <- res
		}()
		<-engine.entered // whichever item owns the claim is in the engine
		synctest.Wait()  // the sibling is attached, not a fake duplicate

		engine.failOne.Store(true)
		engine.release()
		res := <-out
		if len(res.Duplicates) != 0 || len(res.Succeeded) != 0 {
			t.Fatalf("result = %+v, want no success/duplicate on owner failure", res)
		}
		if len(res.Errors) != 2 || res.Errors["key-a"] == "" || res.Errors["key-b"] == "" {
			t.Fatalf("Errors = %v, want both item keys recorded", res.Errors)
		}
		if res.Errors["key-a"] != res.Errors["key-b"] {
			t.Fatalf("loser error %q diverged from owner error %q", res.Errors["key-b"], res.Errors["key-a"])
		}

		// The owner's provisional marks were cleaned: an explicit resubmit of
		// the same batch content retries instead of being stuck.
		engine.disarm()
		res2, err := svc.AddPreparedExtractorItems(context.Background(), req)
		if err != nil {
			t.Fatalf("resubmit err = %v", err)
		}
		if len(res2.Succeeded) != 1 || len(res2.Duplicates) != 1 {
			t.Fatalf("resubmit = %+v, want 1 success + 1 duplicate", res2)
		}
		if got := engine.addCount(); got != 2 {
			t.Fatalf("engine adds = %d, want 2 total", got)
		}
	})
}

func TestServiceAddDedup_LoserContextCancellation(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svcA := &Service{Engine: engine, Deduper: d}
	svcB := &Service{Engine: engine, Deduper: d}
	svcC := &Service{Engine: engine, Deduper: d}
	url := "http://127.0.0.1/files/cancel.bin"

	call := func(svc *Service, ctx context.Context, id string, out chan<- PreparedAddResult) {
		go func() {
			res, _ := svc.AddPreparedDirectItems(ctx, PreparedDirectAddRequest{
				Items: []PreparedDirectAddItem{{ClientItemID: id, URL: url}},
			})
			out <- res
		}()
	}

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		outA := make(chan PreparedAddResult, 1)
		call(svcA, context.Background(), "id-owner", outA)
		<-engine.entered

		ctxB, cancelB := context.WithCancel(context.Background())
		outB := make(chan PreparedAddResult, 1)
		call(svcB, ctxB, "id-cancel", outB)
		outC := make(chan PreparedAddResult, 1)
		call(svcC, context.Background(), "id-waiter", outC)
		synctest.Wait()

		cancelB()
		resB := <-outB
		if len(resB.Errors) != 1 || resB.Errors["id-cancel"] != context.Canceled.Error() {
			t.Fatalf("cancelled loser errors = %v, want context.Canceled", resB.Errors)
		}
		if got := engine.addCount(); got != 1 {
			t.Fatalf("engine adds = %d, want 1 (owner untouched)", got)
		}
		if !deduperHasKey(d, url) {
			t.Fatal("cancelled waiter must not delete the owner claim")
		}

		engine.release()
		resA := <-outA
		if len(resA.Succeeded) != 1 || resA.Succeeded[0] != "id-owner" {
			t.Fatalf("owner result = %+v, want success", resA)
		}
		resC := <-outC
		if len(resC.Duplicates) != 1 || resC.Duplicates[0] != "id-waiter" {
			t.Fatalf("surviving waiter result = %+v, want duplicate", resC)
		}
	})
}

func TestServiceAddDedup_NilDeduperIndependentServicesEachAdd(t *testing.T) {
	setupDedupEnv(t)
	engine := &dedupGateEngine{} // no gate: adds complete immediately
	svcA := &Service{Engine: engine}
	svcB := &Service{Engine: engine}
	url := "http://127.0.0.1/files/nil-compat.bin"

	if got := svcA.AddUri(url); got != "success" {
		t.Fatalf("nil service A AddUri = %q, want success", got)
	}
	if got := svcB.AddUri(url); got != "success" {
		t.Fatalf("nil service B AddUri = %q, want success (no hidden global dedup)", got)
	}
	if got := engine.addCount(); got != 2 {
		t.Fatalf("engine adds = %d, want 2", got)
	}
}

func TestServiceAddDedup_OwnerExistingDuplicateWakesWaiters(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	url := "http://127.0.0.1/files/already-there.bin"
	existing := []rpc.Task{{
		GID:   "gid-existing",
		Files: []rpc.File{{Uris: []rpc.Uri{{Uri: url}}}},
	}}
	engA := &dedupGateEngine{active: existing}
	engB := &dedupGateEngine{} // empty snapshot for the loser side
	svcA := &Service{Engine: engA, Deduper: d}
	svcB := &Service{Engine: engB, Deduper: d}

	synctest.Test(t, func(t *testing.T) {
		engA.tellEntered = make(chan struct{}, 8)
		engA.tellGate = make(chan struct{})
		// Extension seam: acquire happens before the snapshot read, so the
		// owner blocks inside the claim while the loser attaches.
		resA := make(chan extAddResult, 1)
		callExtensionAdd(svcA, extension.DownloadRequest{URL: url}, resA)
		<-engA.tellEntered // owner holds the claim inside its snapshot read

		resB := make(chan extAddResult, 1)
		callExtensionAdd(svcB, extension.DownloadRequest{URL: url}, resB)
		synctest.Wait()

		close(engA.tellGate)
		gotA := <-resA
		if gotA.err == nil || gotA.err.Error() != "duplicate" || gotA.gid != "gid-existing" {
			t.Fatalf("owner-dedup result = %+v, want duplicate gid-existing", gotA)
		}
		gotB := <-resB
		if gotB.err == nil || gotB.err.Error() != "duplicate" || gotB.gid != "gid-existing" {
			t.Fatalf("waiter result = %+v, want available duplicate gid-existing", gotB)
		}
		if got := engA.addCount() + engB.addCount(); got != 0 {
			t.Fatalf("engine adds = %d, want 0", got)
		}

		// Batch seam: a resolved candidate URL already in the snapshot dedups
		// at claim check and publishes available — later callers hit the
		// record. A fresh registry is required: reusing d would short-circuit
		// on part 1's live done-record for the same key and skip the
		// owner-side snapshot check entirely.
		d2 := &AddDeduper{}
		engC := &dedupGateEngine{active: existing}
		svcSnap := &Service{
			Engine:  engC,
			Deduper: d2,
			Adapter: &refRecordingAdapter{fakePortAdapter: fakePortAdapter{items: []ResolvedItem{{URL: url, Ref: "ry"}}}},
		}
		if got := svcSnap.AddUri("http://127.0.0.1/source/page"); got != "duplicate" {
			t.Fatalf("snapshot-dedup AddUri = %q, want duplicate", got)
		}
		if got := engC.addCount(); got != 0 {
			t.Fatalf("engine adds = %d, want 0 for snapshot dedup", got)
		}
		svcLate := &Service{Engine: engB, Deduper: d2}
		if got := svcLate.AddUri(url); got != "duplicate" {
			t.Fatalf("late AddUri = %q, want duplicate via done-record", got)
		}
		if got := engB.addCount(); got != 0 {
			t.Fatalf("engine adds = %d, want 0", got)
		}
	})
}

func TestServiceAddDedup_StaleSnapshotDoneRecordBlocks(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	// Tell* intentionally stay empty: the engine-side list cache lag must not
	// reopen the race while the done-record is fresh.
	engine := &dedupGateEngine{}
	svcA := &Service{Engine: engine, Deduper: d}
	svcB := &Service{Engine: engine, Deduper: d}
	url := "http://127.0.0.1/files/stale-snap.bin"

	if got := svcA.AddUri(url); got != "success" {
		t.Fatalf("first AddUri = %q, want success", got)
	}
	if got := svcB.AddUri(url); got != "duplicate" {
		t.Fatalf("second AddUri = %q, want duplicate via done-record", got)
	}
	if got := engine.addCount(); got != 1 {
		t.Fatalf("engine adds = %d, want 1", got)
	}
}

func TestServiceAddDedup_ThroughputDistinctKeys(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svc := &Service{Engine: engine, Deduper: d}

	const n = 8
	items := make([]PreparedDirectAddItem, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, PreparedDirectAddItem{
			ClientItemID: fmt.Sprintf("tp-%d", i),
			URL:          fmt.Sprintf("http://127.0.0.1/files/tp-%d.bin", i),
		})
	}

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 16)
		engine.resetGate()
		out := make(chan PreparedAddResult, 1)
		go func() {
			res, _ := svc.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{Items: items})
			out <- res
		}()

		for i := 0; i < n; i++ {
			<-engine.entered
		}
		synctest.Wait()
		if got := engine.addCount(); got != n {
			t.Fatalf("engine adds = %d, want %d distinct-key adds in-flight", got, n)
		}
		if got := engine.peakInFlight(); got != n {
			t.Fatalf("engine peak = %d, want %d concurrent (no global serialization)", got, n)
		}
		engine.release()
		res := <-out
		if len(res.Succeeded) != n {
			t.Fatalf("Succeeded = %v, want %d", res.Succeeded, n)
		}
	})
}

func TestServiceAddDedup_ErrorRedactionAcrossServices(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	adA := &refRecordingAdapter{fakePortAdapter: fakePortAdapter{items: []ResolvedItem{
		{URL: "http://127.0.0.1/files/secret.bin", Ref: "sec-ref"},
	}}}
	svcA := &Service{Engine: engine, Adapter: adA, Deduper: d}
	svcB := &Service{Engine: engine, Deduper: d} // nil adapter: relies on owner's redaction
	svcExt := &Service{Engine: engine, Deduper: d}

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		// A credential-bearing error must reach the loser already redacted by
		// the owner's adapter.
		engine.failErr = errors.New("upstream rejected https://x.invalid/a?token=sekrit-value")
		resA := make(chan string, 1)
		go func() { resA <- svcA.AddUri("http://127.0.0.1/source/page") }()
		<-engine.entered

		resB := make(chan string, 1)
		go func() { resB <- svcB.AddUri("http://127.0.0.1/files/secret.bin") }()
		synctest.Wait()
		engine.failOne.Store(true)
		engine.release()
		gotA, gotB := <-resA, <-resB
		for i, got := range []string{gotA, gotB} {
			if strings.Contains(got, "sekrit-value") {
				t.Fatalf("result %d leaked secret: %q", i, got)
			}
		}
		if gotB != gotA {
			t.Fatalf("loser error %q != owner error %q", gotB, gotA)
		}

		// Sentinel identity survives publication for a cross-entry loser.
		engine.resetGate()
		engine.failErr = surgetypes.ErrInsufficientDiskSpace
		url2 := "http://127.0.0.1/files/sentinel.bin"
		resA2 := make(chan string, 1)
		// nil-adapter service: the source URL is the candidate key itself.
		go func() { resA2 <- svcB.AddUri(url2) }()
		<-engine.entered

		resExt := make(chan extAddResult, 1)
		callExtensionAdd(svcExt, extension.DownloadRequest{URL: url2}, resExt)
		synctest.Wait()
		engine.failOne.Store(true)
		engine.release()
		got := <-resExt
		if !errors.Is(got.err, surgetypes.ErrInsufficientDiskSpace) {
			t.Fatalf("extension loser err = %v, want ErrInsufficientDiskSpace identity", got.err)
		}
		<-resA2
	})
}

func TestServiceAddDedup_PanicSettlesWaitersAndAllowsRetry(t *testing.T) {
	setupDedupEnv(t)
	d := &AddDeduper{}
	engine := &dedupGateEngine{}
	svcA := &Service{Engine: engine, Deduper: d}
	svcB := &Service{Engine: engine, Deduper: d}
	url := "http://127.0.0.1/files/panic.bin"

	synctest.Test(t, func(t *testing.T) {
		engine.entered = make(chan struct{}, 8)
		engine.resetGate()
		panicVal := make(chan any, 1)
		go func() {
			defer func() { panicVal <- recover() }()
			_ = svcA.AddUri(url)
		}()
		<-engine.entered

		resB := make(chan string, 1)
		go func() { resB <- svcB.AddUri(url) }()
		synctest.Wait()

		engine.panicOne.Store(true)
		engine.release()

		if v := <-panicVal; v == nil {
			t.Fatal("owner panic must propagate to the outer goroutine")
		}
		gotB := <-resB
		if gotB == "success" || gotB == "duplicate" || !strings.Contains(gotB, "add claim aborted") {
			t.Fatalf("waiter got %q, want the fixed aborted error", gotB)
		}
		if deduperHasKey(d, url) {
			t.Fatal("panicked claim must leave no residue")
		}

		engine.disarm()
		if got := svcB.AddUri(url); got != "success" {
			t.Fatalf("post-panic retry = %q, want success", got)
		}
	})
}
