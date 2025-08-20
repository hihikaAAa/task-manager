package main

import (
	"os"
    "log"
    "time"

    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
    "github.com/hihikaAAa/task-manager/internal/config"
    "github.com/hihikaAAa/task-manager/internal/lib"
    "github.com/hihikaAAa/task-manager/internal/storage/sqlite"
)

func main() {
    cfgPath := os.Getenv("CONFIG_PATH")
    if cfgPath == "" { 
		cfgPath = "config/local.yaml" 
	}

    cfg, err := config.MustLoad(cfgPath)
    if err != nil { 
		log.Fatal("load config:", err)
	 }

    db, err := sqlite.Open(cfg.DBPath)
    if err != nil { 
		log.Fatal("open db:", err) 
	}

    botAPI, err := tgbotapi.NewBotAPI(cfg.BotToken)
    if err != nil {
		 log.Fatal("bot:", err) 
		}
    botAPI.Debug = false

    loc := time.Local
    if cfg.Timezone != "" {
        if l, err := time.LoadLocation(cfg.Timezone); err == nil { loc = l }
    }

    bot := lib.NewBot(botAPI, db, cfg.BossIDs, loc)

    log.Printf("Bot started as @%s with config %s", botAPI.Self.UserName, cfgPath)
    if err := bot.Start(); err != nil { 
		log.Fatal(err) 
	}
}