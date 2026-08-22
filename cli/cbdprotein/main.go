package main

import (
	"net/http"
	"os"

	"github.com/kaz/pprotein/integration/echov4"
	"github.com/kaz/pprotein/internal/collect"
	"github.com/kaz/pprotein/internal/collect/group"
	"github.com/kaz/pprotein/internal/event"
	"github.com/kaz/pprotein/internal/extproc/alp"
	"github.com/kaz/pprotein/internal/extproc/slp"
	"github.com/kaz/pprotein/internal/memo"
	"github.com/kaz/pprotein/internal/notify"
	"github.com/kaz/pprotein/internal/pprof"
	"github.com/kaz/pprotein/internal/storage"
	"github.com/kaz/pprotein/view"
	"github.com/labstack/echo/v4"
)

func start() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	store, err := storage.New("data")
	if err != nil {
		return err
	}

	e := echo.New()
	echov4.Integrate(e)

	fs, err := view.FS()
	if err != nil {
		return err
	}
	e.GET("/*", echo.WrapHandler(http.FileServer(http.FS(fs))))

	api := e.Group("/api", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Cache-Control", "no-store")
			return next(c)
		}
	})

	hub := event.NewHub()
	hub.RegisterHandlers(api.Group("/event"))

	notifier := notify.NewSlack()
	hook := func(entry *collect.Entry) {
		notifier.Report(notify.Result{
			Type:     entry.Snapshot.Type,
			Label:    entry.Snapshot.Label,
			GroupId:  entry.Snapshot.GroupId,
			Status:   string(entry.Status),
			Message:  entry.Message,
			Datetime: entry.Snapshot.Datetime,
		})
	}

	pprofOpts := &collect.Options{
		Type:     "pprof",
		Ext:      "-pprof.pb.gz",
		Store:    store,
		EventHub: hub,
		Hook:     hook,
	}
	if err := pprof.NewHandler(pprofOpts).Register(api.Group("/pprof")); err != nil {
		return err
	}

	alpOpts := &collect.Options{
		Type:     "httplog",
		Ext:      "-httplog.log",
		Store:    store,
		EventHub: hub,
		Hook:     hook,
	}
	alpHandler, err := alp.NewHandler(alpOpts, store)
	if err != nil {
		return err
	}
	if err := alpHandler.Register(api.Group("/httplog")); err != nil {
		return err
	}

	slpOpts := &collect.Options{
		Type:     "slowlog",
		Ext:      "-slowlog.log",
		Store:    store,
		EventHub: hub,
		Hook:     hook,
	}
	slpHandler, err := slp.NewHandler(slpOpts, store)
	if err != nil {
		return err
	}
	if err := slpHandler.Register(api.Group("/slowlog")); err != nil {
		return err
	}

	memoOpts := &collect.Options{
		Type:     "memo",
		Ext:      "-memo.log",
		Store:    store,
		EventHub: hub,
		Hook:     hook,
	}
	if err := memo.NewHandler(memoOpts).Register(api.Group("/memo")); err != nil {
		return err
	}

	grp, err := group.NewCollector(store, port, notifier)
	if err != nil {
		return err
	}
	grp.RegisterHandlers(api.Group("/group"))

	return e.Start(":" + port)
}

func main() {
	if err := start(); err != nil {
		panic(err)
	}
}
