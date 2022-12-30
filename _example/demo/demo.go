package main

import (
	"fmt"
	"github.com/snail007/gocron"
	"net/http"
	"sync/atomic"
	"time"
)

func main() {
	var i = new(int32)
	gocron.AddJob(gocron.Job{
		CronExp:     "*/1 * * * * *",
		Description: "demodemodemodemodemodemodemodemodemodemodemodemo",
		Executor: func() {
			k := atomic.AddInt32(i, 1)
			fmt.Println(k)
			time.Sleep(time.Second * 3)
			fmt.Println(k, "end")
		},
		Mutex: false,
	})
	http.HandleFunc("/crontab/",gocron.HandlerFunc())
	http.ListenAndServe(":8800",nil)
}
