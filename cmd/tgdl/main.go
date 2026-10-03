// Command tgdl is a Telegram bot that downloads from many sources and uploads
// to Telegram and file hosts.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot/models"

	tgbot "github.com/go-telegram/bot"

	"github.com/burhanverse/tgdl/internal/apk"
	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/bot"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/jobs"
	"github.com/burhanverse/tgdl/internal/logging"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/torrent"
	"github.com/burhanverse/tgdl/internal/upload"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	closeLog := logging.Setup(cfg)
	defer closeLog()
	if err := cfg.Validate(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	authz := auth.New(cfg)
	authz.WarnIfUnrestricted()

	var app *bot.App
	b, api, err := bot.NewClient(cfg, authz, func(ctx context.Context, b *tgbot.Bot, u *models.Update) {
		if app != nil {
			app.Default(ctx, b, u)
		}
	})
	if err != nil {
		return fmt.Errorf("create telegram client: %w", err)
	}

	keys := upload.NewKeys(cfg)
	hosts := upload.NewHosts(cfg, keys)
	trackers := torrent.NewTrackers(cfg.DataDir)
	aria := torrent.NewAria2(cfg, trackers)
	search := torrent.NewMagnetio(cfg)
	lim := pacing.NewTelegramLimiter()
	runner := dl.NewRunner(cfg, keys)

	deps := &jobs.Deps{
		Cfg: cfg, Store: st, TG: tg.Client(api), Keys: keys, Hosts: hosts, Runner: runner,
		Aria: aria, Xenforo: dl.NewXenforo(cfg, search), Patcher: apk.New(cfg),
	}
	mgr := jobs.NewManager(deps, lim)
	app = bot.New(bot.Deps{Cfg: cfg, Auth: authz, Store: st, Mgr: mgr, Keys: keys, Hosts: hosts,
		Search: search, Lim: lim, Runner: runner}, b, api)

	if err := api.SetCommands(ctx, commands()); err != nil {
		slog.Warn("could not set bot commands", "err", err)
	}

	// Sidecars start in the background so the bot is responsive immediately.
	go search.Start(ctx)
	go func() {
		if err := aria.Start(ctx); err != nil {
			slog.Warn("aria2 daemon unavailable", "err", err)
		}
	}()

	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("start job manager: %w", err)
	}
	slog.Info("bot is active and listening for messages")
	b.Start(ctx) // blocks until ctx is cancelled

	slog.Info("shutting down")
	mgr.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	aria.Stop(shutdownCtx)
	search.Stop()
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func commands() []tg.Command {
	return []tg.Command{
		{Name: "m", Description: "Mirror file/URL to GoFile, FileDitch & Pixeldrain"},
		{Name: "dl", Description: "Download direct HTTP link"},
		{Name: "aria", Description: "Download link or torrent using aria2"},
		{Name: "tor", Description: "Download torrent or magnet link"},
		{Name: "ts", Description: "Search torrents across indexers"},
		{Name: "gdl", Description: "Download URLs using gallery-dl"},
		{Name: "cdl", Description: "Download URLs using cyberdrop-dl"},
		{Name: "xenforo", Description: "Download media from a forum thread"},
		{Name: "mega", Description: "Download file or folder from MEGA"},
		{Name: "gdlconf", Description: "Manage your gallery-dl config and cookies"},
		{Name: "gd2tg", Description: "Download Google Drive link to Telegram"},
		{Name: "gofile", Description: "Upload replied media to GoFile"},
		{Name: "fileditch", Description: "Upload replied media to FileDitch"},
		{Name: "pdup", Description: "Upload replied media to Pixeldrain"},
		{Name: "patch", Description: "Decompile, patch, sign & upload an APK"},
		{Name: "setkeystore", Description: "Set your JKS keystore for signing"},
		{Name: "unzip", Description: "Download & extract an archive"},
		{Name: "status", Description: "Show active tasks"},
		{Name: "cancel", Description: "Cancel active or queued jobs"},
		{Name: "help", Description: "Show the help guide"},
		{Name: "start", Description: "Start the bot"},
	}
}
