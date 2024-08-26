package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/szczursonn/muter2024/muterbot"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type config struct {
	debug  bool
	prefix string
	tokens []string
}

func getEnv(key string) string {
	return os.Getenv(fmt.Sprintf("MUTER_%s", key))
}

func loadConfig() (cfg config) {
	godotenv.Overload()

	tokensRaw := getEnv("TOKENS")
	if tokensRaw == "" {
		cfg.tokens = []string{}
	} else {
		cfg.tokens = strings.Split(tokensRaw, ",")
	}
	cfg.prefix = getEnv("PREFIX")
	if cfg.prefix == "" {
		cfg.prefix = "$"
	}
	cfg.debug = getEnv("DEBUG") != ""

	return cfg
}

func setupLogger(debugMode bool) {
	cores := []zapcore.Core{}
	opts := []zap.Option{}

	var atomicLevel zap.AtomicLevel
	if debugMode {
		atomicLevel = zap.NewAtomicLevelAt(zap.DebugLevel)
	} else {
		atomicLevel = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	consoleEncoderConfig := zap.NewDevelopmentEncoderConfig()
	consoleEncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	cores = append(cores, zapcore.NewCore(zapcore.NewConsoleEncoder(consoleEncoderConfig), zapcore.Lock(os.Stderr), atomicLevel))

	if debugMode {
		f, _, err := zap.Open("./debug.txt")
		if err != nil {
			panic(err)
		}

		cores = append(cores, zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), zapcore.Lock(f), atomicLevel))
		opts = append(opts, zap.Development())
	}

	zap.ReplaceGlobals(zap.New(zapcore.NewTee(cores...), opts...))
}

func main() {
	cfg := loadConfig()
	setupLogger(cfg.debug)
	defer zap.S().Sync()

	if cfg.debug {
		zap.S().Debugln("Debug on")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	defer stop()
	doneChan := make(chan error)

	zap.S().Infof("Starting MuterBot, Prefix: %s, %d total clients", cfg.prefix, len(cfg.tokens))
	go muterbot.RunMuterBot(ctx, doneChan, cfg.tokens, cfg.prefix)

	go func() {
		<-ctx.Done()
		zap.S().Info("Shutting down...")

		<-time.After(5 * time.Second)
		zap.S().Fatal("Shutdown timed out")
	}()

	err := <-doneChan

	if err != nil {
		zap.S().Error(err)
	} else {
		zap.S().Info("Shut down without error")
	}
}
