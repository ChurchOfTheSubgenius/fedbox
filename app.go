package fedbox

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"git.sr.ht/~mariusor/lw"
	w "git.sr.ht/~mariusor/wrapper"
	vocab "github.com/go-ap/activitypub"
	"github.com/go-ap/auth"
	"github.com/go-ap/client"
	"github.com/go-ap/errors"
	ap "github.com/go-ap/fedbox/activitypub"
	"github.com/go-ap/fedbox/internal/cache"
	"github.com/go-ap/fedbox/internal/config"
	st "github.com/go-ap/fedbox/storage"
	"github.com/go-ap/processing"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/openshift/osin"
)

func init() {
	// set local path typer to validate collections
	processing.Typer = pathTyper{}
}

type LogFn func(string, ...interface{})

type FedBOX struct {
	R            chi.Router
	conf         config.Options
	self         vocab.Service
	client       client.C
	storage      FullStorage
	ver          string
	caches       cache.CanStore
	OAuth        authService
	keyGenerator func(act *vocab.Actor) error
	stopFn       func()
	logger       lw.Logger
}

var (
	emptyFieldsLogFn = func(lw.Ctx, string, ...interface{}) {}
	emptyLogFn       = func(string, ...interface{}) {}
	emptyStopFn      = func() {}
	InfoLogFn        = func(l lw.Logger) func(lw.Ctx, string, ...interface{}) {
		if l == nil {
			return emptyFieldsLogFn
		}
		return func(f lw.Ctx, s string, p ...interface{}) { l.WithContext(f).Infof(s, p...) }
	}
	ErrLogFn = func(l lw.Logger) func(lw.Ctx, string, ...interface{}) {
		if l == nil {
			return emptyFieldsLogFn
		}
		return func(f lw.Ctx, s string, p ...interface{}) { l.WithContext(f).Errorf(s, p...) }
	}
)

var AnonymousAcct = account{
	username: "anonymous",
	actor:    &auth.AnonymousActor,
}

var InternalIRI = vocab.IRI("https://fedbox/")

// New instantiates a new FedBOX instance
func New(l lw.Logger, ver string, conf config.Options, db FullStorage) (*FedBOX, error) {
	if db == nil {
		return nil, errors.Newf("invalid storage")
	}
	if conf.BaseURL == "" {
		return nil, errors.Newf("invalid empty BaseURL config")
	}
	app := FedBOX{
		ver:     ver,
		conf:    conf,
		R:       chi.NewRouter(),
		storage: db,
		stopFn:  emptyStopFn,
		logger:  l,
		caches:  cache.New(conf.RequestCache),
	}

	if metaSaver, ok := db.(st.MetadataTyper); ok {
		keysType := "ED25519"
		if conf.MastodonCompatible {
			keysType = "RSA"
		}

		l.Infof("Setting actor key generator %T[%s]", metaSaver, keysType)
		app.keyGenerator = AddKeyToPerson(metaSaver, keysType)
	}

	errors.IncludeBacktrace = conf.LogLevel == lw.TraceLevel

	selfIRI := ap.DefaultServiceIRI(conf.BaseURL)
	app.self, _ = ap.LoadActor(db, selfIRI)
	if app.self.ID != selfIRI {
		app.infFn("trying to bootstrap the instance's self service")
		if saver, ok := db.(st.CanBootstrap); ok {
			app.self = ap.Self(selfIRI)
			if err := saver.CreateService(app.self); err != nil {
				app.errFn("unable to save the instance's self service: %s", err)
				return nil, err
			}
		}
		keysType := KeyTypeED25519
		if conf.MastodonCompatible {
			keysType = KeyTypeRSA
		}
		if saver, ok := db.(st.MetadataTyper); ok {
			if err := AddKeyToPerson(saver, keysType)(&app.self); err != nil {
				app.errFn("unable to save the instance's self service public key: %s", err)
			}
		}
	}

	app.client = *client.New(
		client.WithLogger(l.WithContext(lw.Ctx{"log": "client"})),
		client.SkipTLSValidation(!conf.Env.IsProd()),
	)

	as, err := auth.New(
		auth.WithURL(conf.BaseURL),
		auth.WithStorage(app.storage),
		auth.WithClient(&app.client),
		auth.WithLogger(l.WithContext(lw.Ctx{"log": "osin"})),
	)
	if err != nil {
		l.Warnf(err.Error())
		return nil, err
	}

	app.R.Use(middleware.RequestID)
	app.R.Use(lw.Middlewares(l)...)

	baseIRI := app.self.GetLink()
	app.OAuth = authService{
		baseIRI: baseIRI,
		auth:    *as,
		genID:   GenerateID(baseIRI),
		storage: app.storage,
		logger:  l.WithContext(lw.Ctx{"log": "auth-service"}),
	}

	app.R.Group(app.Routes())

	return &app, err
}

func (f *FedBOX) Config() config.Options {
	return f.conf
}

func (f *FedBOX) Storage() FullStorage {
	return f.storage
}

// Stop
func (f *FedBOX) Stop() {
	if st, ok := f.storage.(osin.Storage); ok {
		st.Close()
	}
	f.stopFn()
}

func (f *FedBOX) reload() (err error) {
	f.conf, err = config.LoadFromEnv(f.conf.Env, f.conf.TimeOut)
	f.caches.Remove()
	return err
}

func (f *FedBOX) actorFromRequest(r *http.Request) vocab.Actor {
	act, err := f.OAuth.auth.LoadActorFromAuthHeader(r)
	if err != nil {
		f.logger.Errorf("unable to load an authorized Actor from request: %+s", err)
	}
	return act
}

// Run is the wrapper for starting the web-server and handling signals
func (f *FedBOX) Run(c context.Context) error {
	// Create a deadline to wait for.
	ctx, cancelFn := context.WithTimeout(c, f.conf.TimeOut)
	defer cancelFn()

	sockType := ""
	setters := []w.SetFn{w.Handler(f.R)}

	if f.conf.Secure {
		if len(f.conf.CertPath)+len(f.conf.KeyPath) > 0 {
			setters = append(setters, w.WithTLSCert(f.conf.CertPath, f.conf.KeyPath))
		} else {
			f.conf.Secure = false
		}
	}

	if f.conf.Listen == "systemd" {
		sockType = "Systemd"
		setters = append(setters, w.OnSystemd())
	} else if filepath.IsAbs(f.conf.Listen) {
		dir := filepath.Dir(f.conf.Listen)
		if _, err := os.Stat(dir); err == nil {
			sockType = "socket"
			setters = append(setters, w.OnSocket(f.conf.Listen))
			defer func() { os.RemoveAll(f.conf.Listen) }()
		}
	} else {
		sockType = "TCP"
		setters = append(setters, w.OnTCP(f.conf.Listen))
	}
	logCtx := lw.Ctx{
		"URL":      f.conf.BaseURL,
		"version":  f.ver,
		"listenOn": f.conf.Listen,
		"TLS":      f.conf.Secure,
	}
	if sockType != "" {
		logCtx["listenOn"] = f.conf.Listen + "[" + sockType + "]"
	}

	// Get start/stop functions for the http server
	srvRun, srvStop := w.HttpServer(setters...)
	logger := f.logger.WithContext(logCtx)
	logger.Infof("Started")
	f.stopFn = func() {
		if err := srvStop(ctx); err != nil {
			logger.Errorf(err.Error())
		}
	}

	exit := w.RegisterSignalHandlers(w.SignalHandlers{
		syscall.SIGHUP: func(_ chan int) {
			logger.Infof("SIGHUP received, reloading configuration")
			if err := f.reload(); err != nil {
				logger.Errorf("Failed: %+s", err.Error())
			}
		},
		syscall.SIGINT: func(exit chan int) {
			logger.Infof("SIGINT received, stopping")
			exit <- 0
		},
		syscall.SIGTERM: func(exit chan int) {
			logger.Infof("SIGITERM received, force stopping")
			exit <- 0
		},
		syscall.SIGQUIT: func(exit chan int) {
			logger.Infof("SIGQUIT received, force stopping with core-dump")
			exit <- 0
		},
	}).Exec(func() error {
		if err := srvRun(); err != nil {
			logger.Errorf(err.Error())
			return err
		}
		var err error
		// Doesn't block if no connections, but will otherwise wait until the timeout deadline.
		go func(e error) {
			logger.Errorf(err.Error())
			f.stopFn()
		}(err)
		return err
	})
	if exit == 0 {
		logger.Infof("Shutting down")
	}
	return nil
}

func (f *FedBOX) infFn(s string, p ...any) {
	if f.logger != nil {
		f.logger.Infof(s, p...)
	}
}

func (f *FedBOX) errFn(s string, p ...any) {
	if f.logger != nil {
		f.logger.Errorf(s, p...)
	}
}
