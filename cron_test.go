package gocron_test

import (
	"github.com/snail007/gmc"
	gcore "github.com/snail007/gmc/core"
	ghttp "github.com/snail007/gmc/util/http"
	"github.com/snail007/gocron"
	assert0 "github.com/stretchr/testify/assert"
	"net"
	"testing"
	"time"
)

func assert(t *testing.T) *assert0.Assertions {
	return assert0.New(t)
}

func TestCrontabManager_Start(t *testing.T) {
	t.Parallel()
	mc := gocron.NewCrontabManager().Start()
	cnt := 0
	mc.AddJob(gocron.Job{
		CronExp:     "* * * * * *", // every seconds
		Description: "test every seconds panic job",
		Executor: func() {
			cnt++
		},
	})
	time.Sleep(time.Millisecond * 3000)
	assert(t).Equal(3, cnt)
}

func TestJobList(t *testing.T) {
	t.Parallel()
	mc := gocron.NewCrontabManager().Start()
	for i := 0; i < 10; i++ {
		mc.AddJob(gocron.Job{
			CronExp:     "* * * * *",
			Description: "test list",
			Executor: func() {
			},
		})
	}
	assert(t).Len(mc.JobList(), 10)
}

func TestTriggerJob(t *testing.T) {
	t.Parallel()
	mc := gocron.NewCrontabManager().Start()
	cnt := 0
	for i := 0; i < 10; i++ {
		mc.AddJob(gocron.Job{
			CronExp:     "00 00 00 * * *",
			Description: "test list",
			Executor: func() {
				cnt++
			},
		})
	}
	mc.TriggerJob(9)
	time.Sleep(time.Second)
	assert(t).Equal(1, cnt)

}

func TestRemoveJob(t *testing.T) {
	t.Parallel()
	mc := gocron.NewCrontabManager().Start()
	cnt := 0
	for i := 0; i < 10; i++ {
		mc.AddJob(gocron.Job{
			CronExp:     "00 00 00 * * *",
			Description: "test list",
			Executor: func() {
				cnt++
			},
		})
	}
	assert(t).Len(mc.JobList(), 10)
	mc.RemoveJob(9)
	assert(t).Len(mc.JobList(), 9)
}

func TestGetJob1(t *testing.T) {
	t.Parallel()
	mc := gocron.NewCrontabManager().Start()
	cnt := 0
	for i := 0; i < 10; i++ {
		mc.AddJob(gocron.Job{
			CronExp:     "00 00 00 * * *",
			Description: "test list",
			Executor: func() {
				cnt++
			},
		})
	}
	assert(t).NotNil(mc.GetJob(2))
}

func TestGetJob2(t *testing.T) {
	t.Parallel()
	mc := gocron.NewCrontabManager().Start()
	cnt := 0
	for i := 0; i < 10; i++ {
		mc.AddJob(gocron.Job{
			CronExp:     "00 00 00 * * *",
			Description: "test list",
			Executor: func() {
				cnt++
			},
		})
	}
	assert(t).Nil(mc.GetJob(11))
}

func TestCrontabManager_HandlerFunc(t *testing.T) {
	mc := gocron.NewCrontabManager().Start()
	api, _ := gmc.New.APIServer(gmc.New.Ctx(), ":")
	api.API("/cron/*path", func(ctx gcore.Ctx) {
		mc.HandlerFunc()(ctx.Response(), ctx.Request())
	})
	err := api.Run()
	assert(t).NoError(err)
	_, port, _ := net.SplitHostPort(api.Address())
	body, _, _, err := ghttp.Get("http://127.0.0.1:"+port+"/cron/joblist.json", time.Second, nil)
	assert(t).NoError(err)
	assert(t).JSONEq(`{"code":200,"data":[],"msg":""}`, string(body))
}
