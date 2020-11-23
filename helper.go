package main

import (
	"fmt"
	"io/ioutil"
	"os"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/BurntSushi/toml"
)

type ConfigMapper struct {
	GlobalConfig GlobalConfig
	GCPConfig    GCPConfig
}

type GlobalConfig struct {
	LogPath string `toml:"log_path"`
}

type GCPConfig struct {
	ProjectID      string `toml:"project_id"`
	ProjectName    string `toml:"project_name"` //not used, just for visibility
	RegionName     string `toml:"region_name"`
	GKEClusterName string `toml:"gke_cluster_name"`
}

func NewConfig() (*ConfigMapper, error) {
	var err error
	c, err := openConfigToml("~/.mock-google-cloud-sdk-compute.toml")
	if err != nil {
		return nil, err
	}
	return c, nil
}

func openConfigToml(file string) (*ConfigMapper, error) {
	b, err := ioutil.ReadFile((file))
	if err != nil {
		//fmt.Printf("INFO: useing default config, due to unable to load config toml file(%s).\n", file)
		return initConfigAsDefault(), nil
	}

	c := &ConfigMapper{}
	_, err = toml.Decode(string(b), c)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to decode to toml")
	}

	return c, nil
}

func initConfigAsDefault() *ConfigMapper {
	return &ConfigMapper{
		GlobalConfig: GlobalConfig{
			LogPath: "/tmp/mock-google-cloud-sdk-compute.log",
		},
	}
}

type LogMapper struct {
	*logrus.Logger
}

func (log LogMapper) FatalWithError(message string, err error) {
	log.WithFields(logrus.Fields{"error_message": err}).Fatal(message)
}

func NewLog(config *ConfigMapper) (*LogMapper, error) {
	logrus, err := NewStdoutLoggerWithFile(config)
	if err != nil {
		return nil, err
	}
	return &LogMapper{logrus}, nil
}

type LogrusFileHook struct {
	file      *os.File
	flag      int
	chmod     os.FileMode
	formatter *logrus.JSONFormatter
}

func NewStdoutLoggerWithFile(config *ConfigMapper) (*logrus.Logger, error) {
	logrus := &logrus.Logger{
		Out:       os.Stdout,
		Formatter: &logrus.TextFormatter{ForceColors: true},
		Hooks:     make(logrus.LevelHooks),
		// Minimum level to log at (5 is most verbose (debug), 0 is panic)
		Level: logrus.DebugLevel,
	}
	fileHook, err := NewLogrusFileHook(config.GlobalConfig.LogPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)

	if err == nil {
		logrus.Hooks.Add(fileHook)
	}
	return logrus, err
}

func NewLogrusFileHook(file string, flag int, chmod os.FileMode) (*LogrusFileHook, error) {
	jsonFormatter := &logrus.JSONFormatter{}
	logFile, err := os.OpenFile(file, flag, chmod)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to write file on filehook")
	}
	return &LogrusFileHook{logFile, flag, chmod, jsonFormatter}, err
}

// interface fulfillment for Hook type
func (hook *LogrusFileHook) Fire(entry *logrus.Entry) error {
	format, err := hook.formatter.Format(entry)
	if err != nil {
		return err
	}
	line := string(format)
	_, err = hook.file.WriteString(line)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to write file on filehook(entry.String)%v", err)
		return err
	}

	return nil
}

// interface fulfillment for Hook type
func (hook *LogrusFileHook) Levels() []logrus.Level {
	return []logrus.Level{
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
		logrus.DebugLevel,
	}
}
