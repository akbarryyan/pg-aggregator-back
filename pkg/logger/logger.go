package logger

import (
	"log"
	"os"
)

type Logger struct {
	info  *log.Logger
	warn  *log.Logger
	error *log.Logger
	debug *log.Logger
}

var Log *Logger

func Init(level string) {
	Log = &Logger{
		info:  log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile),
		warn:  log.New(os.Stdout, "WARN: ", log.Ldate|log.Ltime|log.Lshortfile),
		error: log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile),
		debug: log.New(os.Stdout, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile),
	}
}

func (l *Logger) Info(v ...interface{}) {
	l.info.Println(v...)
}

func (l *Logger) Infof(format string, v ...interface{}) {
	l.info.Printf(format, v...)
}

func (l *Logger) Warn(v ...interface{}) {
	l.warn.Println(v...)
}

func (l *Logger) Warnf(format string, v ...interface{}) {
	l.warn.Printf(format, v...)
}

func (l *Logger) Error(v ...interface{}) {
	l.error.Println(v...)
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	l.error.Printf(format, v...)
}

func (l *Logger) Debug(v ...interface{}) {
	l.debug.Println(v...)
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	l.debug.Printf(format, v...)
}

func Info(v ...interface{}) {
	if Log != nil {
		Log.Info(v...)
	}
}

func Infof(format string, v ...interface{}) {
	if Log != nil {
		Log.Infof(format, v...)
	}
}

func Warn(v ...interface{}) {
	if Log != nil {
		Log.Warn(v...)
	}
}

func Warnf(format string, v ...interface{}) {
	if Log != nil {
		Log.Warnf(format, v...)
	}
}

func Error(v ...interface{}) {
	if Log != nil {
		Log.Error(v...)
	}
}

func Errorf(format string, v ...interface{}) {
	if Log != nil {
		Log.Errorf(format, v...)
	}
}

func Debug(v ...interface{}) {
	if Log != nil {
		Log.Debug(v...)
	}
}

func Debugf(format string, v ...interface{}) {
	if Log != nil {
		Log.Debugf(format, v...)
	}
}
