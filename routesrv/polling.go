package routesrv

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	ot "github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
	"github.com/zalando/skipper/eskip"
	"github.com/zalando/skipper/filters/auth"
	"github.com/zalando/skipper/predicates"
	"github.com/zalando/skipper/routing"
	"github.com/zalando/skipper/tracing"
)

const (
	LogPollingStarted       = "starting polling"
	LogPollingStopped       = "polling stopped"
	LogRoutesFetchingFailed = "failed to fetch routes"
	LogRoutesEmpty          = "received empty routes; ignoring"
	LogRoutesInitialized    = "routes initialized"
	LogRoutesUpdated        = "routes updated"
)

var (
	pollingStarted = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "routesrv",
		Name:      "polling_started_timestamp",
		Help:      "UNIX time when the routes polling has started",
	})
	routesInitialized = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "routesrv",
		Name:      "routes_initialized_timestamp",
		Help:      "UNIX time when the first routes were received and stored",
	})
	routesUpdated = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "routesrv",
		Name:      "routes_updated_timestamp",
		Help:      "UNIX time of the last routes update (initial load counts as well)",
	})
)

type poller struct {
	client  routing.DataClient
	b       *eskipBytes
	timeout time.Duration
	quit    chan struct{}

	// Preprocessors
	defaultFilters  *eskip.DefaultFilters
	oauth2Config    *auth.OAuthConfig
	editRoute       []*eskip.Editor
	cloneRoute      []*eskip.Clone
	disabledFilters []string

	// tracer
	tracer ot.Tracer
}

func (p *poller) poll(wg *sync.WaitGroup) {
	defer wg.Done()

	var (
		routesCount, routesBytes int
		initialized              bool
		msg                      string
	)

	log.WithField("timeout", p.timeout).Info(LogPollingStarted)
	ticker := time.NewTicker(p.timeout)
	defer ticker.Stop()
	pollingStarted.SetToCurrentTime()

	for {
		span := tracing.CreateSpan("poll_routes", context.TODO(), p.tracer)

		routes, err := p.client.LoadAll()

		routes = p.process(routes)

		routesCount = len(routes)

		switch {
		case err != nil:
			log.WithError(err).Error(LogRoutesFetchingFailed)

			span.SetTag("error", true)
			span.LogKV(
				"event", "error",
				"message", fmt.Sprintf("%s: %s", LogRoutesFetchingFailed, err),
			)
		case routesCount == 0:
			log.Error(LogRoutesEmpty)

			span.SetTag("error", true)
			span.LogKV(
				"event", "error",
				"message", msg,
			)
		case routesCount > 0:
			routesBytes, initialized = p.b.formatAndSet(routes)
			logger := log.WithFields(log.Fields{"count": routesCount, "bytes": routesBytes})
			if initialized {
				logger.Info(LogRoutesInitialized)
				span.SetTag("routes.initialized", true)
				routesInitialized.SetToCurrentTime()
			} else {
				logger.Info(LogRoutesUpdated)
			}
			routesUpdated.SetToCurrentTime()
			span.SetTag("routes.count", routesCount)
			span.SetTag("routes.bytes", routesBytes)
		}

		span.Finish()

		select {
		case <-p.quit:
			log.Info(LogPollingStopped)
			return
		case <-ticker.C:
		}
	}
}

func (p *poller) process(routes []*eskip.Route) []*eskip.Route {

	if p.defaultFilters != nil {
		routes = p.defaultFilters.Do(routes)
	}
	if p.oauth2Config != nil {
		routes = p.oauth2Config.NewGrantPreprocessor().Do(routes)
	}
	for _, editor := range p.editRoute {
		routes = editor.Do(routes)
	}

	for _, cloner := range p.cloneRoute {
		routes = cloner.Do(routes)
	}

	hasDisabledFilters := len(p.disabledFilters) != 0

	routes = p.validateRoutes(routes, hasDisabledFilters)

	// sort the routes, otherwise it will lead to different etag values for the same route list for different orders
	sort.SliceStable(routes, func(i, j int) bool {
		return routes[i].Id < routes[j].Id
	})

	return routes
}

func (p *poller) validateRoutes(routes []*eskip.Route, hasDisabledFilters bool) []*eskip.Route {
	validRoutes := make([]*eskip.Route, 0, len(routes))

	var disabledFiltersMap map[string]struct{}

	if hasDisabledFilters {
		disabledFiltersMap = toMap(p.disabledFilters)
	}

	var validRouteFilters bool
	for _, r := range routes {
		validRouteFilters = true
		for _, filter := range r.Filters {
			if filter.Name == predicates.PathSubtreeName || filter.Name == predicates.PathName || filter.Name == predicates.HostName || filter.Name == predicates.PathRegexpName || filter.Name == predicates.MethodName || filter.Name == predicates.HeaderName || filter.Name == predicates.HeaderRegexpName {
				validRouteFilters = false
				log.Errorf("trying to use %q as filter, but it is only available as predicate", filter.Name)
				break
			}

			if hasDisabledFilters {
				if _, ok := disabledFiltersMap[filter.Name]; ok {
					validRouteFilters = false
					log.Errorf("trying to use %q filter, which is disabled", filter.Name)
					break
				}
			}
		}
		if validRouteFilters {
			validRoutes = append(validRoutes, r)
		}
	}

	return validRoutes
}

func toMap[C comparable](values []C) map[C]struct{} {
	casted := make(map[C]struct{}, 0)
	for _, value := range values {
		casted[value] = struct{}{}
	}
	return casted
}
