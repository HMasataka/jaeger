package jaeger

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/uber/jaeger-client-go/config"
)

const defaultComponentName = "chi"

// TraceConfig defines the config for Trace middleware.
type TraceConfig struct {
	// OpenTracing Tracer instance which should be got before
	Tracer opentracing.Tracer

	// ComponentName used for describing the tracing component name
	ComponentName string

	// add req body & resp body to tracing tags
	IsBodyDump bool

	// prevent logging long http request bodies
	LimitHTTPBody bool

	// http body limit size (in bytes)
	// NOTE: don't specify values larger than 60000 as jaeger can't handle values in span.LogKV larger than 60000 bytes
	LimitSize int

	// OperationNameFunc composes operation name based on context. Can be used to override default naming
	OperationNameFunc func(r *http.Request) string
}

func New(r chi.Router) io.Closer {
	defaultConfig := config.Configuration{
		ServiceName: "chi-tracer",
		Sampler: &config.SamplerConfig{
			Type:  "const",
			Param: 1,
		},
		Reporter: &config.ReporterConfig{
			LogSpans:            true,
			BufferFlushInterval: 1 * time.Second,
		},
	}

	cfg, err := defaultConfig.FromEnv()
	if err != nil {
		panic("Could not parse Jaeger env vars: " + err.Error())
	}

	tracer, closer, err := cfg.NewTracer()
	if err != nil {
		panic("Could not initialize jaeger tracer: " + err.Error())
	}
	opentracing.SetGlobalTracer(tracer)

	r.Use(TraceWithConfig(TraceConfig{
		Tracer: tracer,
	}))

	return closer
}

func TraceWithConfig(config TraceConfig) func(next http.Handler) http.Handler {
	if config.Tracer == nil {
		panic("echo: trace middleware requires opentracing tracer")
	}
	if config.ComponentName == "" {
		config.ComponentName = defaultComponentName
	}
	if config.OperationNameFunc == nil {
		config.OperationNameFunc = defaultOperationName
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			operationName := config.OperationNameFunc(r)

			realIP := ""
			requestID := getRequestID(r)

			var sp opentracing.Span
			var err error

			ctx, err := config.Tracer.Extract(
				opentracing.HTTPHeaders,
				opentracing.HTTPHeadersCarrier(r.Header),
			)

			if err != nil {
				sp = config.Tracer.StartSpan(operationName)
			} else {
				sp = config.Tracer.StartSpan(operationName, ext.RPCServerOption(ctx))
			}
			defer sp.Finish()

			ext.HTTPMethod.Set(sp, chi.RouteContext(r.Context()).RouteMethod)
			ext.HTTPUrl.Set(sp, chi.RouteContext(r.Context()).RoutePattern())
			ext.Component.Set(sp, config.ComponentName)
			sp.SetTag("client_ip", realIP)
			sp.SetTag("request_id", requestID)

			// Dump request & response body
			var respDumper *responseDumper

			if config.IsBodyDump {
				reqBody := []byte{}
				if r.Body != nil {
					reqBody, _ = io.ReadAll(r.Body)

					if config.LimitHTTPBody {
						sp.LogKV("http.req.body", limitString(string(reqBody), config.LimitSize))
					} else {
						sp.LogKV("http.req.body", string(reqBody))
					}
				}

				r.Body = io.NopCloser(bytes.NewBuffer(reqBody)) // reset original request body

				// response
				respDumper = newResponseDumper(w)
				w = respDumper
			}

			r = r.WithContext(opentracing.ContextWithSpan(r.Context(), sp))

			// inject Jaeger context into request header
			_ = config.Tracer.Inject(sp.Context(), opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(r.Header))

			// call next middleware / controller
			next.ServeHTTP(w, r)

			ww := middleware.NewWrapResponseWriter(w, 1)
			status := ww.Status()
			ext.HTTPStatusCode.Set(sp, uint16(status))

			if err != nil {
				logError(sp, err)
			}

			// Dump response body
			if config.IsBodyDump {
				if config.LimitHTTPBody {
					sp.LogKV("http.resp.body", limitString(respDumper.GetResponse(), config.LimitSize))
				} else {
					sp.LogKV("http.resp.body", respDumper.GetResponse())
				}
			}
		})
	}
}

func limitString(str string, size int) string {
	if len(str) > size {
		return str[:size/2] + "\n---- skipped ----\n" + str[len(str)-size/2:]
	}

	return str
}

func logError(span opentracing.Span, err error) {
	span.LogKV("error.message", err.Error())
	span.SetTag("error", true)
}

func getRequestID(r *http.Request) string {
	// TODO proxy で設定した値を取得
	requestID := generateToken() // missed request-id from proxy, we generate it manually
	return requestID
}

func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func defaultOperationName(r *http.Request) string {
	path := chi.RouteContext(r.Context()).RoutePattern()
	return " URL: " + path
}
