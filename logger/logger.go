// +build !windows

package logger

import (
	"log"
	"log/syslog"
	"os"
	"io"
)

const (
	LogLevelOff = iota
	LogLevelFatal
	LogLevelError
	LogLevelWarn
	LogLevelInfo
	LogLevelInfoA
	LogLevelDebug
)

var (
	LEVEL int = LogLevelOff
	AppName string = "rabbit-mtcp-ws"
	UseSyslog bool = true
)

type Logger struct {
	logger *log.Logger
	level  int
}

func NewLogger(prefix string) *Logger {

	if UseSyslog {
		sysLog, err := syslog.New(syslog.LOG_INFO|syslog.LOG_LOCAL0, AppName)
		if err != nil {
			log.Fatal(err)
		}
		// 创建多写入器，同时写入syslog和标准输出
		multiWriter := io.MultiWriter(os.Stdout, sysLog)
		log.SetOutput(multiWriter)
	
		return &Logger{
			logger: log.New(multiWriter, prefix, log.LstdFlags),
			level:  LEVEL,
		}
	} else {
		return &Logger{
			logger: log.New(os.Stdout, prefix, log.LstdFlags),
			level:  LEVEL,
		}
	}
}

func (l *Logger) Logln(v string) {
	l.logger.Println(v)

}
func (l *Logger) Logf(format string, v ...interface{}) {
	l.logger.Printf(format, v...)
}


func (l *Logger) Debugln(v string) {
	if l.level >= LogLevelDebug {
		l.logger.Println("[Debug] " + v)
	}
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	if l.level >= LogLevelDebug {
		l.logger.Printf("[Debug] "+format, v...)
	}
}

func (l *Logger) InfoAln(v string) {
	if l.level >= LogLevelInfoA {
		l.logger.Println("[Info] " + v)
	}
}

func (l *Logger) InfoAf(format string, v ...interface{}) {
	if l.level >= LogLevelInfoA {
		l.logger.Printf("[Info] "+format, v...)
	}
}


func (l *Logger) Infoln(v string) {
	if l.level >= LogLevelInfo {
		l.logger.Println("[Info] " + v)
	}
}

func (l *Logger) Infof(format string, v ...interface{}) {
	if l.level >= LogLevelInfo {
		l.logger.Printf("[Info] "+format, v...)
	}
}

func (l *Logger) Warnln(v string) {
	if l.level >= LogLevelWarn {
		l.logger.Println("[Warn] " + v)
	}
}

func (l *Logger) Warnf(format string, v ...interface{}) {
	if l.level >= LogLevelWarn {
		l.logger.Printf("[Warn] "+format, v...)
	}
}

func (l *Logger) Errorln(v string) {
	if l.level >= LogLevelError {
		l.logger.Println("[Error] " + v)
	}
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	if l.level >= LogLevelError {
		l.logger.Printf("[Error] "+format, v...)
	}
}

func (l *Logger) Fatalln(v string) {
	if l.level >= LogLevelFatal {
		l.logger.Println("[Fatal] " + v)
	}
}

func (l *Logger) Fatalf(format string, v ...interface{}) {
	if l.level >= LogLevelFatal {
		l.logger.Printf("[Fatal] "+format, v...)
	}
}
