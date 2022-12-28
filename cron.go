package gocron

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"
	gcore "github.com/snail007/gmc/core"
	gctx "github.com/snail007/gmc/module/ctx"
	gerror "github.com/snail007/gmc/module/error"
	glog "github.com/snail007/gmc/module/log"
	gcast "github.com/snail007/gmc/util/cast"
	gfile "github.com/snail007/gmc/util/file"
	gmap "github.com/snail007/gmc/util/map"
	gonce "github.com/snail007/gmc/util/sync/once"
)

var (
	defaultCrontabManager = NewCrontabManager().Start()
	//go:embed index.gohtml
	indexTpl string
	//go:embed jquery.js
	jquery string
)

func HandlerFilter(job Job) func(ctx gcore.Ctx) error {
	return defaultCrontabManager.HandlerFilter()
}

func SetHandlerFilter(handlerFilter func(ctx gcore.Ctx) error) {
	defaultCrontabManager.SetHandlerFilter(handlerFilter)
}

func AddJob(job Job) (jobID int, err error) {
	return defaultCrontabManager.AddJob(job)
}

func RemoveJob(jobID int) (err error) {
	defaultCrontabManager.RemoveJob(jobID)
	return
}

func TriggerJob(jobID int) {
	defaultCrontabManager.TriggerJob(jobID)
}

func JobList() []*JobItem {
	return defaultCrontabManager.JobList()
}

func GetJob(jobID int) (jobItem *JobItem) {
	return defaultCrontabManager.GetJob(jobID)
}

func HandlerFunc() http.HandlerFunc {
	return defaultCrontabManager.HandlerFunc()
}

type Job struct {
	CronExp      string
	Description  string
	Executor     func()
	Mutex        bool
	TriggerAt    time.Time
	entryID      cron.EntryID
	runningCount *int32
}

type JobItem struct {
	JobID        int    `json:"job_id"`
	CronExp      string `json:"cron_exp"`
	Description  string `json:"description"`
	PrevAt       int64  `json:"prev_at"`
	NextAt       int64  `json:"next_at"`
	TriggerAt    int64  `json:"trigger_at"`
	RunningCount int64  `json:"running_count"`
}

type CrontabManager struct {
	c             *cron.Cron
	jobs          *gmap.Map
	l             sync.Mutex
	tpl           *template.Template
	handlerFilter func(ctx gcore.Ctx) error
}

func (s *CrontabManager) HandlerFilter() func(ctx gcore.Ctx) error {
	return s.handlerFilter
}

func (s *CrontabManager) SetHandlerFilter(handlerFilter func(ctx gcore.Ctx) error) {
	s.handlerFilter = handlerFilter
}

func (s *CrontabManager) mutexFunc(job *Job) {
	f := job.Executor
	if !job.Mutex {
		job.Executor = func() {
			defer func() {
				atomic.AddInt32(job.runningCount, -1)
			}()
			atomic.AddInt32(job.runningCount, 1)
			f()
		}
	} else {
		var lock = new(int32)
		job.Executor = func() {
			if !atomic.CompareAndSwapInt32(lock, 0, 1) {
				//pre task is running, skip this round.
				//fmt.Println("skipped")
				return
			}
			defer func() {
				atomic.AddInt32(job.runningCount, -1)
				atomic.StoreInt32(lock, 0)
			}()
			atomic.AddInt32(job.runningCount, 1)
			f()
		}
	}
}

func (s *CrontabManager) AddJob(job Job) (jobID int, err error) {
	s.l.Lock()
	defer s.l.Unlock()
	job.runningCount = new(int32)
	s.mutexFunc(&job)
	id, err := s.c.AddFunc(job.CronExp, job.Executor)
	if err != nil {
		return
	}
	job.entryID = id
	s.jobs.Store(int(id), &job)
	jobID = int(id)
	return
}

func (s *CrontabManager) RemoveJob(jobID int) (err error) {
	s.l.Lock()
	defer s.l.Unlock()
	s.c.Remove(cron.EntryID(jobID))
	s.jobs.Delete(jobID)
	return
}

func (s *CrontabManager) Start() *CrontabManager {
	s.c.Start()
	return s
}

func (s *CrontabManager) Stop() *CrontabManager {
	s.c.Stop()
	return s
}

func (s *CrontabManager) JobList() (jobItems []*JobItem) {
	jobItems = []*JobItem{}
	s.jobs.RangeFast(func(jobID, jobI interface{}) bool {
		jobItems = append(jobItems, s.job2item(jobI.(*Job)))
		return true
	})
	return
}

func (s *CrontabManager) GetJob(jobID int) (jobItem *JobItem) {
	j, ok := s.jobs.Load(jobID)
	if !ok {
		return nil
	}
	if j != nil {
		jobItem = s.job2item(j.(*Job))
	}
	return
}
func (s *CrontabManager) job2item(job *Job) *JobItem {
	entry := s.c.Entry(job.entryID)
	prev := entry.Prev.Unix()
	if prev < 0 {
		prev = 0
	}
	triggerAt := int64(0)
	if !job.TriggerAt.IsZero() {
		triggerAt = job.TriggerAt.Unix()
	}
	return &JobItem{
		JobID:        int(job.entryID),
		CronExp:      job.CronExp,
		Description:  job.Description,
		PrevAt:       prev,
		NextAt:       entry.Next.Unix(),
		TriggerAt:    triggerAt,
		RunningCount: int64(atomic.LoadInt32(job.runningCount)),
	}
}

func (s *CrontabManager) ExistsJob(jobID int) bool {
	_, ok := s.jobs.Load(jobID)
	return ok
}

func (s *CrontabManager) TriggerJob(jobID int) (err error) {
	job, ok := s.jobs.Load(jobID)
	if !ok {
		return fmt.Errorf("job %d not found", jobID)
	}
	job.(*Job).TriggerAt = time.Now()
	go func() {
		defer gerror.Recover(func(e gcore.Error) {
			err = fmt.Errorf("%s", e.ErrorStack())
		})
		s.c.Entry(cron.EntryID(jobID)).WrappedJob.Run()
	}()
	return nil
}

func (s *CrontabManager) HandlerFunc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := gctx.NewCtxWithHTTP(w, r)
		if s.handlerFilter != nil {
			e := s.handlerFilter(ctx)
			if e != nil {
				ctx.Write(httpData(500, nil, e.Error()))
				return
			}
		}
		var err error
		var data interface{}
		switch gfile.BaseName(r.URL.Path) {
		case "joblist":
			data, err = s.handleJobList(ctx)
		case "joblist.json":
			data, err = s.handleJobListJson(ctx)
		case "triggerjob":
			data, err = s.handleTriggerJob(ctx)
		case "demolist":
			data, err = s.handleJobList(ctx)
		default:
			err = fmt.Errorf("operate unsupported")
		}
		if err != nil {
			ctx.Write(httpData(500, nil, err.Error()))
			return
		}
		if data != nil {
			ctx.Write(httpData(200, data, ""))
		}
	}
}

func (s *CrontabManager) handleJobList(ctx gcore.Ctx) (_ interface{}, err error) {
	gonce.OnceDo("crontab-foo", func() {
		s.tpl = template.New("crontab")
		s.tpl.Funcs(map[string]interface{}{
			"date": func(args ...interface{}) (interface{}, error) {
				t := gcast.ToString(args[1])
				if t == "0" {
					return args[2], nil
				}
				return time.Unix(gcast.ToInt64(t), 0).Format(args[0].(string)), nil
			},
		})
		_, err = s.tpl.Parse(indexTpl)
	})
	if err != nil {
		return
	}
	tplData := map[string]interface{}{
		"rows":   s.JobList(),
		"jquery": jquery,
	}
	err = s.tpl.Execute(ctx.Response(), tplData)
	return
}

func (s *CrontabManager) handleJobListJson(_ gcore.Ctx) (data interface{}, err error) {
	return s.JobList(), nil
}

func (s *CrontabManager) handleTriggerJob(ctx gcore.Ctx) (data interface{}, err error) {
	jobID := gcast.ToInt(ctx.GET("jobid"))
	if jobID == 0 {
		err = fmt.Errorf("job id error")
		return
	}
	if !s.ExistsJob(jobID) {
		err = fmt.Errorf("job %d not exists", jobID)
		return
	}
	go func() {
		defer func() {
			if e := recover(); e != nil {
				DefaultLogger.Printf("[warn] trigger job %d panic, error: %v", jobID, gerror.New().New(e).ErrorStack())
			}
		}()
		s.TriggerJob(jobID)
	}()
	return "success", err
}

func NewCrontabManager() *CrontabManager {
	l := cron.PrintfLogger(DefaultLogger)
	//l := cron.VerbosePrintfLogger(DefaultLogger)
	c := cron.New(
		// support of seconds field, optional
		cron.WithParser(cron.NewParser(
			cron.SecondOptional|cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor,
		)),
		cron.WithLogger(l),
		// panic recovery and configure the panic logger, and skip if still running.
		cron.WithChain(cron.Recover(l)),
	)

	return &CrontabManager{
		c:    c,
		jobs: gmap.New(),
	}
}

var (
	DefaultLogger = NewLogger()
)

type logger struct {
	l gcore.Logger
}

func NewLogger() *logger {
	l0 := &logger{l: glog.New()}
	l0.l.SetCallerSkip(l0.l.CallerSkip() + 2)
	l0.l.SetFlag(gcore.LogFlagShort)
	return l0
}

func (l *logger) Printf(fmtstr string, msg ...interface{}) {
	l.l.Write(fmt.Sprintf(fmtstr, msg...))
}

func httpData(code int, data, msg interface{}) interface{} {
	return gmap.M{
		"code": code,
		"data": data,
		"msg":  msg,
	}
}
