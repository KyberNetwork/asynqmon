package asynqmon

import (
	"embed"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gorilla/mux"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// instanceIDPattern restricts RedisInstance IDs to URL-safe identifiers, since
// they are used as an API path segment and in PromQL label matchers.
var instanceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// RedisInstance describes a single named redis connection that asynqmon can
// monitor. When more than one instance is configured, the Web UI shows a
// dropdown to switch between them and the API is served under
// /api/instances/{id}/...
type RedisInstance struct {
	// ID is a unique identifier used in the API path
	// (e.g. /api/instances/{id}/queues). It must match [a-zA-Z0-9_-]+.
	//
	// This field is required.
	ID string

	// Name is a human-readable label shown in the Web UI instance selector.
	//
	// This field is optional. Defaults to ID.
	Name string

	// RedisConnOpt specifies the connection to a redis-server or redis-cluster.
	//
	// This field is required.
	RedisConnOpt asynq.RedisConnOpt
}

// Options are used to configure HTTPHandler.
type Options struct {
	// URL path the handler is responsible for.
	// The path is used for the homepage of asynqmon, and every other page is rooted in this subtree.
	//
	// This field is optional. Default is "/".
	RootPath string

	// RedisConnOpt specifies the connection to a redis-server or redis-cluster.
	//
	// This field is required unless RedisInstances is set. When RedisInstances
	// is non-empty, this field is ignored.
	RedisConnOpt asynq.RedisConnOpt

	// RedisInstances specifies multiple named redis connections to monitor.
	//
	// This field is optional. If set (non-empty), it takes precedence over
	// RedisConnOpt and the Web UI exposes an instance selector. If empty,
	// RedisConnOpt is used as a single instance with ID "default".
	RedisInstances []RedisInstance

	// PayloadFormatter is used to convert payload bytes to string shown in the UI.
	//
	// This field is optional.
	PayloadFormatter PayloadFormatter

	// ResultFormatter is used to convert result bytes to string shown in the UI.
	//
	// This field is optional.
	ResultFormatter ResultFormatter

	// PrometheusAddress specifies the address of the Prometheus to connect to.
	//
	// This field is optional. If this field is set, asynqmon will query the Prometheus server
	// to get the time series data about queue metrics and show them in the web UI.
	PrometheusAddress string

	// PrometheusInstanceLabel is the name of the label that distinguishes
	// metrics exported for different RedisInstances (e.g. "redis_instance").
	//
	// This field is optional. If set, the metrics view of each instance only
	// queries series whose label value equals that instance's ID. Leave empty
	// when metrics are not labelled per instance (e.g. a single instance).
	PrometheusInstanceLabel string

	// Set ReadOnly to true to restrict user to view-only mode.
	ReadOnly bool
}

// HTTPHandler is a http.Handler for asynqmon application.
type HTTPHandler struct {
	router   *mux.Router
	closers  []func() error
	rootPath string // the value should not have the trailing slash
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

// redisInstance is the internal, fully-initialized representation of a
// RedisInstance with its live redis client and inspector.
type redisInstance struct {
	id        string
	name      string
	rc        redis.UniversalClient
	inspector *asynq.Inspector
}

// New creates a HTTPHandler with the given options.
func New(opts Options) *HTTPHandler {
	// Resolve the configured instances. RedisInstances takes precedence; if it
	// is empty we fall back to the single RedisConnOpt for backward compatibility.
	configured := opts.RedisInstances
	if len(configured) == 0 {
		if opts.RedisConnOpt == nil {
			panic("asynqmon.New: RedisConnOpt or RedisInstances field is required")
		}
		configured = []RedisInstance{{ID: "default", Name: "default", RedisConnOpt: opts.RedisConnOpt}}
	}

	seen := make(map[string]bool, len(configured))
	var instances []redisInstance
	var closers []func() error
	for _, inst := range configured {
		if !instanceIDPattern.MatchString(inst.ID) {
			panic(fmt.Sprintf("asynqmon.New: RedisInstance.ID %q must be a non-empty URL-safe identifier ([a-zA-Z0-9_-]+)", inst.ID))
		}
		if seen[inst.ID] {
			panic(fmt.Sprintf("asynqmon.New: duplicate RedisInstance ID %q", inst.ID))
		}
		seen[inst.ID] = true
		if inst.RedisConnOpt == nil {
			panic(fmt.Sprintf("asynqmon.New: RedisConnOpt is required for instance %q", inst.ID))
		}
		rc, ok := inst.RedisConnOpt.MakeRedisClient().(redis.UniversalClient)
		if !ok {
			panic(fmt.Sprintf("asnyqmon.New: unsupported RedisConnOpt type %T", inst.RedisConnOpt))
		}
		i := asynq.NewInspector(inst.RedisConnOpt)
		name := inst.Name
		if name == "" {
			name = inst.ID
		}
		instances = append(instances, redisInstance{id: inst.ID, name: name, rc: rc, inspector: i})
		closers = append(closers, rc.Close, i.Close)
	}

	// Make sure that RootPath starts with a slash if provided.
	if opts.RootPath != "" && !strings.HasPrefix(opts.RootPath, "/") {
		panic(fmt.Sprintf("asynqmon.New: RootPath must start with a slash"))
	}
	// Remove tailing slash from RootPath.
	opts.RootPath = strings.TrimSuffix(opts.RootPath, "/")

	return &HTTPHandler{
		router:   muxRouter(opts, instances),
		closers:  closers,
		rootPath: opts.RootPath,
	}
}

// Close closes connections to redis. It closes every connection even if some
// fail, and returns all of the errors joined.
func (h *HTTPHandler) Close() error {
	var errs []error
	for _, f := range h.closers {
		if err := f(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RootPath returns the root URL path used for asynqmon application.
// Returned path string does not have the trailing slash.
func (h *HTTPHandler) RootPath() string {
	return h.rootPath
}

//go:embed ui/build/*
var staticContents embed.FS

func muxRouter(opts Options, instances []redisInstance) *mux.Router {
	router := mux.NewRouter().PathPrefix(opts.RootPath).Subrouter()

	var payloadFmt PayloadFormatter = DefaultPayloadFormatter
	if opts.PayloadFormatter != nil {
		payloadFmt = opts.PayloadFormatter
	}

	var resultFmt ResultFormatter = DefaultResultFormatter
	if opts.ResultFormatter != nil {
		resultFmt = opts.ResultFormatter
	}

	api := router.PathPrefix("/api").Subrouter()

	// Instance discovery endpoint for the Web UI selector.
	api.HandleFunc("/instances", newListInstancesHandlerFunc(instances)).Methods("GET")

	// Register the full API surface once per redis instance, scoped under its id.
	for _, inst := range instances {
		sub := api.PathPrefix("/instances/" + inst.id).Subrouter()
		registerAPIHandlers(sub, inst, payloadFmt, resultFmt, opts)
	}

	// Restrict APIs when running in read-only mode.
	if opts.ReadOnly {
		api.Use(restrictToReadOnly)
	}

	// Unknown API paths (e.g. an instance id that is no longer configured)
	// get a plain 404 instead of falling through to the UI assets handler.
	api.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	// Everything else, route to uiAssetsHandler.
	router.NotFoundHandler = &uiAssetsHandler{
		rootPath:       opts.RootPath,
		contents:       staticContents,
		staticDirPath:  "ui/build",
		indexFileName:  "index.html",
		prometheusAddr: opts.PrometheusAddress,
		readOnly:       opts.ReadOnly,
		instances:      instancePayload(instances),
	}

	return router
}

// registerAPIHandlers registers the full asynqmon API surface on the given
// (sub)router, bound to a single redis instance.
func registerAPIHandlers(api *mux.Router, inst redisInstance, payloadFmt PayloadFormatter, resultFmt ResultFormatter, opts Options) {
	rc, inspector := inst.rc, inst.inspector

	// Queue endpoints.
	api.HandleFunc("/queues", newListQueuesHandlerFunc(inspector)).Methods("GET")
	api.HandleFunc("/queues/{qname}", newGetQueueHandlerFunc(inspector)).Methods("GET")
	api.HandleFunc("/queues/{qname}", newDeleteQueueHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}:pause", newPauseQueueHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}:resume", newResumeQueueHandlerFunc(inspector)).Methods("POST")

	// Queue Historical Stats endpoint.
	api.HandleFunc("/queue_stats", newListQueueStatsHandlerFunc(inspector)).Methods("GET")

	// Task endpoints.
	api.HandleFunc("/queues/{qname}/active_tasks", newListActiveTasksHandlerFunc(inspector, payloadFmt)).Methods("GET")
	api.HandleFunc("/queues/{qname}/active_tasks/{task_id}:cancel", newCancelActiveTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/active_tasks:cancel_all", newCancelAllActiveTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/active_tasks:batch_cancel", newBatchCancelActiveTasksHandlerFunc(inspector)).Methods("POST")

	api.HandleFunc("/queues/{qname}/pending_tasks", newListPendingTasksHandlerFunc(inspector, payloadFmt)).Methods("GET")
	api.HandleFunc("/queues/{qname}/pending_tasks/{task_id}", newDeleteTaskHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/pending_tasks:delete_all", newDeleteAllPendingTasksHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/pending_tasks:batch_delete", newBatchDeleteTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/pending_tasks/{task_id}:archive", newArchiveTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/pending_tasks:archive_all", newArchiveAllPendingTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/pending_tasks:batch_archive", newBatchArchiveTasksHandlerFunc(inspector)).Methods("POST")

	api.HandleFunc("/queues/{qname}/scheduled_tasks", newListScheduledTasksHandlerFunc(inspector, payloadFmt)).Methods("GET")
	api.HandleFunc("/queues/{qname}/scheduled_tasks/{task_id}", newDeleteTaskHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/scheduled_tasks:delete_all", newDeleteAllScheduledTasksHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/scheduled_tasks:batch_delete", newBatchDeleteTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/scheduled_tasks/{task_id}:run", newRunTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/scheduled_tasks:run_all", newRunAllScheduledTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/scheduled_tasks:batch_run", newBatchRunTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/scheduled_tasks/{task_id}:archive", newArchiveTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/scheduled_tasks:archive_all", newArchiveAllScheduledTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/scheduled_tasks:batch_archive", newBatchArchiveTasksHandlerFunc(inspector)).Methods("POST")

	api.HandleFunc("/queues/{qname}/retry_tasks", newListRetryTasksHandlerFunc(inspector, payloadFmt)).Methods("GET")
	api.HandleFunc("/queues/{qname}/retry_tasks/{task_id}", newDeleteTaskHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/retry_tasks:delete_all", newDeleteAllRetryTasksHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/retry_tasks:batch_delete", newBatchDeleteTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/retry_tasks/{task_id}:run", newRunTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/retry_tasks:run_all", newRunAllRetryTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/retry_tasks:batch_run", newBatchRunTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/retry_tasks/{task_id}:archive", newArchiveTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/retry_tasks:archive_all", newArchiveAllRetryTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/retry_tasks:batch_archive", newBatchArchiveTasksHandlerFunc(inspector)).Methods("POST")

	api.HandleFunc("/queues/{qname}/archived_tasks", newListArchivedTasksHandlerFunc(inspector, payloadFmt)).Methods("GET")
	api.HandleFunc("/queues/{qname}/archived_tasks/{task_id}", newDeleteTaskHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/archived_tasks:delete_all", newDeleteAllArchivedTasksHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/archived_tasks:batch_delete", newBatchDeleteTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/archived_tasks/{task_id}:run", newRunTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/archived_tasks:run_all", newRunAllArchivedTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/archived_tasks:batch_run", newBatchRunTasksHandlerFunc(inspector)).Methods("POST")

	api.HandleFunc("/queues/{qname}/completed_tasks", newListCompletedTasksHandlerFunc(inspector, payloadFmt, resultFmt)).Methods("GET")
	api.HandleFunc("/queues/{qname}/completed_tasks/{task_id}", newDeleteTaskHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/completed_tasks:delete_all", newDeleteAllCompletedTasksHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/completed_tasks:batch_delete", newBatchDeleteTasksHandlerFunc(inspector)).Methods("POST")

	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks", newListAggregatingTasksHandlerFunc(inspector, payloadFmt)).Methods("GET")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks/{task_id}", newDeleteTaskHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks:delete_all", newDeleteAllAggregatingTasksHandlerFunc(inspector)).Methods("DELETE")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks:batch_delete", newBatchDeleteTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks/{task_id}:run", newRunTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks:run_all", newRunAllAggregatingTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks:batch_run", newBatchRunTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks/{task_id}:archive", newArchiveTaskHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks:archive_all", newArchiveAllAggregatingTasksHandlerFunc(inspector)).Methods("POST")
	api.HandleFunc("/queues/{qname}/groups/{gname}/aggregating_tasks:batch_archive", newBatchArchiveTasksHandlerFunc(inspector)).Methods("POST")

	api.HandleFunc("/queues/{qname}/tasks/{task_id}", newGetTaskHandlerFunc(inspector, payloadFmt, resultFmt)).Methods("GET")

	// Groups endponts
	api.HandleFunc("/queues/{qname}/groups", newListGroupsHandlerFunc(inspector)).Methods("GET")

	// Servers endpoints.
	api.HandleFunc("/servers", newListServersHandlerFunc(inspector, payloadFmt)).Methods("GET")

	// Scheduler Entry endpoints.
	api.HandleFunc("/scheduler_entries", newListSchedulerEntriesHandlerFunc(inspector, payloadFmt)).Methods("GET")
	api.HandleFunc("/scheduler_entries/{entry_id}/enqueue_events", newListSchedulerEnqueueEventsHandlerFunc(inspector)).Methods("GET")

	// Redis info endpoint.
	switch c := rc.(type) {
	case *redis.ClusterClient:
		api.HandleFunc("/redis_info", newRedisClusterInfoHandlerFunc(c, inspector)).Methods("GET")
	case *redis.Client:
		api.HandleFunc("/redis_info", newRedisInfoHandlerFunc(c)).Methods("GET")
	}

	// Time series metrics endpoints.
	var instanceMatcher string
	if opts.PrometheusInstanceLabel != "" {
		instanceMatcher = fmt.Sprintf("%s=%q", opts.PrometheusInstanceLabel, inst.id)
	}
	api.HandleFunc("/metrics", newGetMetricsHandlerFunc(http.DefaultClient, opts.PrometheusAddress, instanceMatcher)).Methods("GET")
}

// restrictToReadOnly is a middleware function to restrict users to perform only GET requests.
func restrictToReadOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "" {
			http.Error(w, fmt.Sprintf("API Server is running in read-only mode: %s request is not allowed", r.Method), http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
}
