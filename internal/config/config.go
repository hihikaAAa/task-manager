package config

import (
    "os"
    "time"
    goyaml "gopkg.in/yaml.v3"
)

type Config struct {
    BotToken string  `yaml:"bot_token"`
    DBPath   string  `yaml:"db_path"`
    BossIDs  []int64 `yaml:"boss_ids"`
    Timezone string  `yaml:"timezone"`
}

func MustLoad(path string) (*Config, error) {
    cfg := &Config{}
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    if err := goyaml.Unmarshal(b, cfg); err != nil {
        return nil, err
    }
    if v := os.Getenv("BOT_TOKEN"); v != "" { cfg.BotToken = v }
    if v := os.Getenv("DB_PATH"); v != "" { cfg.DBPath = v }
    if v := os.Getenv("TZ"); v != "" { 
		cfg.Timezone = v; _ = os.Setenv("TZ", v) 
		} else if cfg.Timezone != "" { 
			_ = os.Setenv("TZ", cfg.Timezone) }
    if cfg.Timezone != "" {
        if loc, err := time.LoadLocation(cfg.Timezone); err == nil {
            time.Local = loc
        }
    }
    return cfg, nil
}
