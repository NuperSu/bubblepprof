// order_pipeline is an end-to-end showcase of bubblepprof instrumentation
// on a realistic-looking workload. It simulates a small order-checkout
// service: a synthetic traffic generator feeds a pool of ingest workers,
// each order fans out into fraud / dispatch / inventory / recommendation
// stages, then funnels through payment authorization and receipt
// rendering, with per-tenant aggregators retaining recent receipts so
// the memusage endpoint has meaningful per-bubble heap.
//
// The standard runtime/pprof endpoints are still mounted at /debug/pprof
// because bubblepprof does not replace pprof; it augments it.
//
// Try it:
//
//	go run ./examples/order_pipeline -duration 30s &
//	curl -X POST http://127.0.0.1:6060/debug/memusage \
//	  -H 'Content-Type: application/json' \
//	  -d '{"labels":{"tenant":"atlas-bikes"}}'
package main

import (
	"bytes"
	"compress/gzip"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NuperSu/bubblepprof/pkg/bubblepprof"
)

type Order struct {
	ID       int64
	Tenant   string
	Region   string
	Tier     string
	Priority string
	CartSize int
}

type Receipt struct {
	OrderID int64
	Tenant  string
	Region  string
	// Payload is the rendered + gzipped receipt body. Retained briefly
	// by the per-tenant aggregator so bubble reports have real bytes
	// to attribute.
	Payload []byte
}

type Notification struct {
	OrderID int64
	Tenant  string
	Channel string
	Outcome string
}

type App struct {
	orders        chan Order
	notifications chan Notification
	receipts      chan Receipt
	ledgerMu      sync.Mutex

	started   atomic.Uint64
	completed atomic.Uint64
	rejected  atomic.Uint64
	dropped   atomic.Uint64
	sink      atomic.Uint64
}

var tenantList = []string{
	"atlas-bikes",
	"noodle-labs",
	"catnip-market",
	"bubble-demo",
}

func main() {
	addr := flag.String("addr", "127.0.0.1:6060", "HTTP address for /debug/memusage, /debug/pprof, and /stats")
	rate := flag.Int("rate", 90, "synthetic orders per second")
	apiWorkers := flag.Int("api-workers", 12, "concurrent order workflow workers")
	notifyWorkers := flag.Int("notify-workers", 5, "async notification workers")
	receiptKeep := flag.Int("receipt-cache", 64, "receipts retained per tenant aggregator")
	duration := flag.Duration("duration", 0, "optional run duration, for example 45s; 0 means run until Ctrl+C")
	flag.Parse()

	if *rate <= 0 {
		log.Fatal("-rate must be positive")
	}
	if *apiWorkers <= 0 || *notifyWorkers <= 0 {
		log.Fatal("worker counts must be positive")
	}
	if *receiptKeep <= 0 {
		log.Fatal("-receipt-cache must be positive")
	}

	// These make non-CPU profiles more interesting when you inspect them.
	runtime.SetBlockProfileRate(1)
	runtime.SetMutexProfileFraction(5)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	app := &App{
		orders:        make(chan Order, 4096),
		notifications: make(chan Notification, 4096),
		receipts:      make(chan Receipt, 4096),
	}

	mux := http.NewServeMux()
	bubblepprof.RegisterMemUsage(mux)
	mux.HandleFunc("/stats", app.statsHandler)
	mux.Handle("/debug/pprof/", http.DefaultServeMux)

	server := &http.Server{Addr: *addr, Handler: mux}

	goWithLabels(ctx, pprof.Labels(
		"component", "observability",
		"role", "http_endpoints",
		"endpoint", "/debug/memusage+/debug/pprof",
	), func(ctx context.Context) {
		log.Printf("memusage endpoint:   curl -X POST http://%s/debug/memusage -H 'Content-Type: application/json' -d '{\"labels\":{\"tenant\":\"atlas-bikes\"}}'", *addr)
		log.Printf("pprof endpoints:     http://%s/debug/pprof/", *addr)
		log.Printf("stats:               http://%s/stats", *addr)

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server error: %v", err)
		}
	})

	for i := 0; i < *apiWorkers; i++ {
		workerID := i
		goWithLabels(ctx, pprof.Labels(
			"component", "api",
			"role", "ingest_worker",
			"worker_id", strconv.Itoa(workerID),
		), func(ctx context.Context) {
			app.ingestWorker(ctx, workerID)
		})
	}

	for i := 0; i < *notifyWorkers; i++ {
		workerID := i
		goWithLabels(ctx, pprof.Labels(
			"component", "async",
			"role", "notification_worker",
			"worker_id", strconv.Itoa(workerID),
		), func(ctx context.Context) {
			app.notificationWorker(ctx, workerID)
		})
	}

	// Per-tenant aggregator goroutines. Each owns the retained receipt
	// buffer for one tenant, so a /debug/memusage query during steady
	// state shows real heap reachable from tenant=X goroutines.
	tenantInboxes := make(map[string]chan Receipt, len(tenantList))
	for _, tenant := range tenantList {
		inbox := make(chan Receipt, 1024)
		tenantInboxes[tenant] = inbox

		t := tenant
		goWithLabels(ctx, pprof.Labels(
			"component", "ledger",
			"role", "tenant_aggregator",
			"tenant", t,
		), func(ctx context.Context) {
			app.tenantAggregator(ctx, t, inbox, *receiptKeep)
		})
	}

	goWithLabels(ctx, pprof.Labels(
		"component", "ledger",
		"role", "receipt_router",
	), func(ctx context.Context) {
		app.routeReceipts(ctx, tenantInboxes)
	})

	goWithLabels(ctx, pprof.Labels(
		"component", "traffic",
		"role", "order_generator",
		"source", "synthetic_load",
	), func(ctx context.Context) {
		app.generateTraffic(ctx, *rate)
	})

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)

	log.Printf(
		"done: started=%d completed=%d rejected=%d dropped=%d sink=%d",
		app.started.Load(),
		app.completed.Load(),
		app.rejected.Load(),
		app.dropped.Load(),
		app.sink.Load(),
	)
}

// goWithLabels stamps pprof labels on a fresh goroutine. It calls
// pprof.SetGoroutineLabels so heap-native label recovery sees the labels
// on the child goroutine.
func goWithLabels(parent context.Context, labels pprof.LabelSet, fn func(context.Context)) {
	ctx := pprof.WithLabels(parent, labels)
	go func() {
		pprof.SetGoroutineLabels(ctx)
		fn(ctx)
	}()
}

func (a *App) statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	fmt.Fprintf(w, "started=%d\n", a.started.Load())
	fmt.Fprintf(w, "completed=%d\n", a.completed.Load())
	fmt.Fprintf(w, "rejected=%d\n", a.rejected.Load())
	fmt.Fprintf(w, "dropped=%d\n", a.dropped.Load())
	fmt.Fprintf(w, "sink=%d\n", a.sink.Load())
	fmt.Fprintf(w, "goroutines=%d\n", runtime.NumGoroutine())
}

func (a *App) generateTraffic(ctx context.Context, rate int) {
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	regions := []string{"eu-west", "eu-central", "us-east", "moon-base"}
	tiers := []string{"free", "pro", "enterprise"}
	priorities := []string{"normal", "express", "recovery"}

	var id int64

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			id++

			order := Order{
				ID:       id,
				Tenant:   weightedChoice(rng, tenantList, []int{35, 25, 25, 15}),
				Region:   weightedChoice(rng, regions, []int{38, 27, 27, 8}),
				Tier:     weightedChoice(rng, tiers, []int{58, 31, 11}),
				Priority: weightedChoice(rng, priorities, []int{72, 22, 6}),
				CartSize: 1 + rng.Intn(12),
			}

			select {
			case a.orders <- order:
			case <-ctx.Done():
				return
			default:
				a.dropped.Add(1)
			}
		}
	}
}

func weightedChoice(rng *rand.Rand, values []string, weights []int) string {
	total := 0
	for _, weight := range weights {
		total += weight
	}

	pick := rng.Intn(total)

	for i, weight := range weights {
		if pick < weight {
			return values[i]
		}
		pick -= weight
	}

	return values[len(values)-1]
}

func (a *App) ingestWorker(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return

		case order := <-a.orders:
			a.handleOrder(ctx, workerID, order)
		}
	}
}

func (a *App) handleOrder(workerCtx context.Context, workerID int, order Order) {
	a.started.Add(1)

	// Label by dimensions you actually want to group into bubbles.
	// pprof.Do pushes onto the goroutine's label stack and pops on return.
	pprof.Do(workerCtx, pprof.Labels(
		"work", "order_checkout",
		"tenant", order.Tenant,
		"region", order.Region,
		"tier", order.Tier,
		"priority", order.Priority,
	), func(orderCtx context.Context) {
		var wg sync.WaitGroup

		var fraudRisk int
		var routeCost int
		var inventoryOK bool
		var recScore int

		wg.Add(4)

		goWithLabels(orderCtx, pprof.Labels(
			"service", "fraud",
			"stage", "score",
			"algorithm", fraudAlgorithm(order),
			"worker_kind", "fanout_task",
		), func(ctx context.Context) {
			defer wg.Done()
			fraudRisk = a.scoreFraud(ctx, order)
		})

		goWithLabels(orderCtx, pprof.Labels(
			"service", "dispatch",
			"stage", "route_plan",
			"algorithm", "grid_dijkstra",
			"worker_kind", "fanout_task",
		), func(ctx context.Context) {
			defer wg.Done()
			routeCost = a.planRoute(ctx, order)
		})

		goWithLabels(orderCtx, pprof.Labels(
			"service", "inventory",
			"stage", "reserve",
			"warehouse", warehouseFor(order.Region),
			"mode", "io_wait",
			"worker_kind", "fanout_task",
		), func(ctx context.Context) {
			defer wg.Done()
			inventoryOK = a.reserveInventory(ctx, order)
		})

		goWithLabels(orderCtx, pprof.Labels(
			"service", "recommendations",
			"stage", "personalize",
			"algorithm", recommenderAlgorithm(order),
			"worker_kind", "fanout_task",
		), func(ctx context.Context) {
			defer wg.Done()
			recScore = a.personalize(ctx, order)
		})

		wg.Wait()

		if fraudRisk > 91 || !inventoryOK {
			a.rejected.Add(1)
			a.publishNotification(orderCtx, Notification{
				OrderID: order.ID,
				Tenant:  order.Tenant,
				Channel: notificationChannel(order),
				Outcome: "rejected",
			})
			return
		}

		approved := false
		pprof.Do(orderCtx, pprof.Labels(
			"service", "payment",
			"stage", "authorize",
			"gateway", paymentGateway(order),
			"lane", paymentLane(order, fraudRisk),
		), func(ctx context.Context) {
			approved = a.authorizePayment(ctx, order, fraudRisk, routeCost)
		})

		if !approved {
			a.rejected.Add(1)
			a.publishNotification(orderCtx, Notification{
				OrderID: order.ID,
				Tenant:  order.Tenant,
				Channel: notificationChannel(order),
				Outcome: "payment_failed",
			})
			return
		}

		var receiptPayload []byte
		pprof.Do(orderCtx, pprof.Labels(
			"service", "fulfillment",
			"stage", "pack",
			"format", "gzip_receipt",
			"printer", receiptPrinter(order.Region),
		), func(ctx context.Context) {
			receiptPayload = a.renderReceipt(ctx, order, workerID, routeCost, recScore)
		})

		a.completed.Add(1)

		// Hand off the rendered receipt to the per-tenant aggregator.
		// Retained bytes live inside the aggregator goroutine, which
		// is labeled with tenant=X — so the bubble report attributes
		// the receipt heap to that tenant.
		select {
		case a.receipts <- Receipt{OrderID: order.ID, Tenant: order.Tenant, Region: order.Region, Payload: receiptPayload}:
		default:
			a.dropped.Add(1)
		}

		a.publishNotification(orderCtx, Notification{
			OrderID: order.ID,
			Tenant:  order.Tenant,
			Channel: notificationChannel(order),
			Outcome: "completed",
		})
	})
}

func (a *App) scoreFraud(ctx context.Context, order Order) int {
	seed := []byte(fmt.Sprintf(
		"%s/%s/%s/%s/%d/%d",
		order.Tenant,
		order.Region,
		order.Tier,
		order.Priority,
		order.CartSize,
		order.ID,
	))

	rounds := 800 + order.CartSize*140
	if order.Tier == "enterprise" {
		rounds += 900
	}
	if order.Priority == "recovery" {
		rounds += 600
	}

	buf := make([]byte, len(seed)+8+32)
	copy(buf, seed)

	var digest [32]byte
	for i := 0; i < rounds; i++ {
		binary.LittleEndian.PutUint64(
			buf[len(seed):],
			uint64(i)*uint64(order.ID+17)+uint64(digest[0]),
		)
		copy(buf[len(seed)+8:], digest[:])
		digest = sha256.Sum256(buf)
	}

	score := int(digest[0]) + int(digest[7]) + int(digest[19]) + order.CartSize*3
	a.sink.Add(uint64(score))
	return score % 100
}

func fraudAlgorithm(order Order) string {
	if order.Tier == "enterprise" || order.Priority == "recovery" {
		return "deep_hash_walk"
	}
	return "fast_hash_walk"
}

func (a *App) planRoute(ctx context.Context, order Order) int {
	width, height := 26, 26
	if order.Region == "moon-base" {
		width, height = 44, 44
	}
	if order.Priority == "express" {
		width += 5
		height += 5
	}

	seed := int(order.ID)%997 +
		len(order.Tenant)*31 +
		len(order.Region)*17 +
		order.CartSize*13

	cost := gridRouteCost(seed, width, height)
	a.sink.Add(uint64(cost))
	return cost
}

type routeNode struct {
	id   int
	cost int
}

type routePQ []routeNode

func (pq routePQ) Len() int           { return len(pq) }
func (pq routePQ) Less(i, j int) bool { return pq[i].cost < pq[j].cost }
func (pq routePQ) Swap(i, j int)      { pq[i], pq[j] = pq[j], pq[i] }
func (pq *routePQ) Push(x any)        { *pq = append(*pq, x.(routeNode)) }
func (pq *routePQ) Pop() any {
	old := *pq
	last := old[len(old)-1]
	*pq = old[:len(old)-1]
	return last
}

func gridRouteCost(seed, width, height int) int {
	n := width * height
	dist := make([]int, n)
	for i := range dist {
		dist[i] = math.MaxInt
	}
	dist[0] = 0

	pq := &routePQ{{id: 0, cost: 0}}
	heap.Init(pq)

	for pq.Len() > 0 {
		item := heap.Pop(pq).(routeNode)

		if item.cost != dist[item.id] {
			continue
		}
		if item.id == n-1 {
			return item.cost
		}

		x := item.id % width
		y := item.id / width

		tryRelax := func(nx, ny int) {
			if nx < 0 || nx >= width || ny < 0 || ny >= height {
				return
			}
			nid := ny*width + nx
			traffic := 1 + positiveMod(
				(nx*37+ny*19+seed*11)^(nx*ny+seed),
				12,
			)
			if positiveMod(nx+ny+seed, 17) == 0 {
				traffic += 14
			}
			newCost := item.cost + traffic
			if newCost < dist[nid] {
				dist[nid] = newCost
				heap.Push(pq, routeNode{id: nid, cost: newCost})
			}
		}

		tryRelax(x+1, y)
		tryRelax(x-1, y)
		tryRelax(x, y+1)
		tryRelax(x, y-1)
	}

	return dist[n-1]
}

func positiveMod(v, m int) int {
	v %= m
	if v < 0 {
		v += m
	}
	return v
}

func (a *App) reserveInventory(ctx context.Context, order Order) bool {
	latency := time.Duration(5+order.CartSize) * time.Millisecond
	if warehouseFor(order.Region) == "warehouse-luna" {
		latency += 12 * time.Millisecond
	}
	time.Sleep(latency)

	reserved := make(map[string]int, order.CartSize)
	checksum := 0
	for i := 0; i < order.CartSize; i++ {
		sku := fmt.Sprintf(
			"sku-%s-%02d",
			order.Tenant[:3],
			(int(order.ID)+i*7)%41,
		)
		reserved[sku]++
		checksum += len(sku) * reserved[sku]
	}
	a.sink.Add(uint64(checksum))
	return positiveMod(checksum+int(order.ID), 23) != 0
}

func warehouseFor(region string) string {
	switch region {
	case "eu-west":
		return "warehouse-dublin"
	case "eu-central":
		return "warehouse-frankfurt"
	case "us-east":
		return "warehouse-virginia"
	case "moon-base":
		return "warehouse-luna"
	default:
		return "warehouse-default"
	}
}

func (a *App) personalize(ctx context.Context, order Order) int {
	size := 70 + order.CartSize*9
	passes := 18
	if order.Tier == "pro" || order.Tier == "enterprise" {
		passes += 16
	}
	if order.Tenant == "catnip-market" {
		passes += 10
	}

	scores := make([]float64, size)
	for pass := 0; pass < passes; pass++ {
		for i := range scores {
			base := float64((i+1)*(pass+3) + int(order.ID)%101)
			scores[i] += math.Sin(base/7.0)*math.Cos(base/13.0) +
				math.Sqrt(float64(i+1))/19.0
		}
	}

	best := 0
	for i, score := range scores {
		if score > scores[best] {
			best = i
		}
	}
	a.sink.Add(uint64(best + int(scores[best]*1000)))
	return best
}

func recommenderAlgorithm(order Order) string {
	if order.Tier == "free" {
		return "tiny_matrix"
	}
	return "medium_matrix"
}

func (a *App) authorizePayment(ctx context.Context, order Order, fraudRisk int, routeCost int) bool {
	if paymentLane(order, fraudRisk) == "slow_review" {
		time.Sleep(9 * time.Millisecond)
	} else {
		time.Sleep(2 * time.Millisecond)
	}

	// Deliberately contended critical section, visible in mutex
	// profiles.
	a.ledgerMu.Lock()
	defer a.ledgerMu.Unlock()

	loops := 3500 + order.CartSize*160
	if order.Priority == "express" {
		loops += 2200
	}

	var x uint64 = uint64(routeCost + fraudRisk + int(order.ID))
	for i := 0; i < loops; i++ {
		x = x*1664525 + 1013904223 + uint64(i)
		x ^= x >> 13
	}
	a.sink.Add(x)
	return positiveMod(int(x)+fraudRisk, 29) != 0
}

func paymentGateway(order Order) string {
	if order.Region == "us-east" {
		return "stripe-ish"
	}
	if order.Region == "moon-base" {
		return "lunar-escrow"
	}
	return "adyen-ish"
}

func paymentLane(order Order, fraudRisk int) string {
	if fraudRisk > 70 || order.Priority == "recovery" {
		return "slow_review"
	}
	return "fast_path"
}

// renderReceipt produces the gzipped receipt body and returns it. The
// caller forwards it to the per-tenant aggregator; the payload stays
// reachable from the aggregator goroutine so /debug/memusage shows real
// bytes attributed to that tenant.
func (a *App) renderReceipt(ctx context.Context, order Order, workerID int, routeCost int, recScore int) []byte {
	payload := map[string]any{
		"order_id":   order.ID,
		"tenant":     order.Tenant,
		"region":     order.Region,
		"tier":       order.Tier,
		"priority":   order.Priority,
		"cart_size":  order.CartSize,
		"route_cost": order.ID%13 + int64(routeCost),
		"rec_slot":   recScore,
		"worker":     workerID,
		"message":    "Thanks for shopping. Your courier is emotionally prepared.",
	}

	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	for i := 0; i < 6+order.CartSize; i++ {
		_ = encoder.Encode(payload)
	}

	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write(raw.Bytes())
	_ = gz.Close()

	a.sink.Add(uint64(compressed.Len() + raw.Len()))
	out := make([]byte, compressed.Len())
	copy(out, compressed.Bytes())
	return out
}

func receiptPrinter(region string) string {
	if region == "moon-base" {
		return "thermal_printer_low_gravity"
	}
	return "thermal_printer_normal"
}

// routeReceipts fans each completed receipt out to the correct tenant
// aggregator's inbox. It runs in its own labeled goroutine so the
// bubble report distinguishes routing heap from aggregator heap.
func (a *App) routeReceipts(ctx context.Context, inboxes map[string]chan Receipt) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-a.receipts:
			inbox, ok := inboxes[r.Tenant]
			if !ok {
				a.dropped.Add(1)
				continue
			}
			select {
			case inbox <- r:
			default:
				a.dropped.Add(1)
			}
		}
	}
}

// tenantAggregator owns the bounded recent-receipts ring for one
// tenant. The ring stays reachable from this goroutine's stack for as
// long as it runs, which gives bubble reports something to attribute
// to "tenant=<name>". keep caps how many receipts the ring retains.
func (a *App) tenantAggregator(ctx context.Context, tenant string, inbox <-chan Receipt, keep int) {
	ring := make([]Receipt, 0, keep)
	var totalBytes uint64

	for {
		select {
		case <-ctx.Done():
			runtime.KeepAlive(ring)
			return

		case r := <-inbox:
			if len(ring) == keep {
				totalBytes -= uint64(len(ring[0].Payload))
				ring = append(ring[:0], ring[1:]...)
			}
			ring = append(ring, r)
			totalBytes += uint64(len(r.Payload))
			a.sink.Add(totalBytes)
		}
	}
}

func (a *App) publishNotification(ctx context.Context, notification Notification) {
	pprof.Do(ctx, pprof.Labels(
		"service", "notifications",
		"stage", "enqueue",
		"channel", notification.Channel,
		"outcome", notification.Outcome,
	), func(ctx context.Context) {
		select {
		case a.notifications <- notification:
		default:
			a.dropped.Add(1)
		}
	})
}

func (a *App) notificationWorker(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return

		case notification := <-a.notifications:
			pprof.Do(ctx, pprof.Labels(
				"service", "notifications",
				"stage", "deliver",
				"channel", notification.Channel,
				"tenant", notification.Tenant,
				"outcome", notification.Outcome,
			), func(ctx context.Context) {
				a.deliverNotification(ctx, workerID, notification)
			})
		}
	}
}

func (a *App) deliverNotification(ctx context.Context, workerID int, notification Notification) {
	switch notification.Channel {
	case "email":
		time.Sleep(7 * time.Millisecond)
	case "push":
		time.Sleep(3 * time.Millisecond)
	case "sms":
		time.Sleep(11 * time.Millisecond)
	}

	var x uint64 = uint64(workerID + len(notification.Tenant) + len(notification.Outcome))
	for i := 0; i < 900; i++ {
		x = (x << 5) ^ (x >> 2) ^ uint64(i*31)
	}
	a.sink.Add(x)
}

func notificationChannel(order Order) string {
	if order.Priority == "express" {
		return "push"
	}
	if order.Tier == "enterprise" {
		return "sms"
	}
	return "email"
}
