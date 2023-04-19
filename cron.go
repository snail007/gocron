package gocron

import (
	"embed"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/robfig/cron/v3"
	_ "github.com/snail007/gmc"
	gcore "github.com/snail007/gmc/core"
	gtemplate "github.com/snail007/gmc/http/template"
	gctx "github.com/snail007/gmc/module/ctx"
	gerror "github.com/snail007/gmc/module/error"
	glog "github.com/snail007/gmc/module/log"
	gcast "github.com/snail007/gmc/util/cast"
	gfile "github.com/snail007/gmc/util/file"
	glist "github.com/snail007/gmc/util/list"
	gmap "github.com/snail007/gmc/util/map"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxMetricsDataLen = 100
	templateExt       = ".gohtml"
)

var (
	defaultCrontabManager = NewCrontabManager().Start()
	//go:embed template/*.gohtml
	templates embed.FS

	//go:embed static/*
	static embed.FS
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
	CronExp     string
	Description string
	Executor    func()
	Mutex       bool
	//MaxMetricsDataLen, value 0 means 100 will be used.
	MaxMetricsDataLen int

	//private
	triggerAt time.Time
	entryID   cron.EntryID
	// need init
	runningCount   *int32
	triggerCount   *uint64
	metricsRunData *glist.List
}

type MetricsRunDataItem struct {
	StartAt time.Time `json:"start_at"`
	EndAt   time.Time `json:"end_at"`
	Skipped bool      `json:"skipped"`
}

func (s *Job) MetricsRunData() (d []MetricsRunDataItem) {
	s.metricsRunData.RangeFast(func(k int, v interface{}) bool {
		value := v.(MetricsRunDataItem)
		d = append(d, value)
		return true
	})
	return d
}

type JobItem struct {
	JobID        int    `json:"job_id"`
	CronExp      string `json:"cron_exp"`
	Description  string `json:"description"`
	PrevAt       int64  `json:"prev_at"`
	NextAt       int64  `json:"next_at"`
	TriggerAt    int64  `json:"trigger_at"`
	RunningCount int64  `json:"running_count"`
	Mutex        bool   `json:"mutex"`
	TriggerCount uint64 `json:"trigger_count"`
	RawJob       *Job   `json:"-"`
}

type CrontabManager struct {
	c             *cron.Cron
	jobs          *gmap.Map
	l             sync.Mutex
	tpl           *gtemplate.Template
	handlerFilter func(ctx gcore.Ctx) error
	initTime      time.Time
}

func (s *CrontabManager) initJob(job *Job) {
	job.runningCount = new(int32)
	job.triggerCount = new(uint64)
	job.metricsRunData = glist.New()
	s.mutexFunc(job)
}

func (s *CrontabManager) HandlerFilter() func(ctx gcore.Ctx) error {
	return s.handlerFilter
}

func (s *CrontabManager) SetHandlerFilter(handlerFilter func(ctx gcore.Ctx) error) {
	s.handlerFilter = handlerFilter
}

func (s *CrontabManager) metricsStart(runCtx *gmap.Map, job *Job) {
	atomic.AddInt32(job.runningCount, 1)
	atomic.AddUint64(job.triggerCount, 1)
	runCtx.Store("start_at", time.Now())
}

func (s *CrontabManager) metricsEnd(runCtx *gmap.Map, job *Job) {
	atomic.AddInt32(job.runningCount, -1)
	startAt, _ := runCtx.Load("start_at")
	_, ok := runCtx.Load("skipped")
	job.metricsRunData.Add(
		MetricsRunDataItem{
			StartAt: startAt.(time.Time),
			EndAt:   time.Now(),
			Skipped: ok,
		})
	if job.MaxMetricsDataLen <= 0 {
		job.MaxMetricsDataLen = maxMetricsDataLen
	}
	if job.metricsRunData.Len() > job.MaxMetricsDataLen {
		job.metricsRunData.Shift()
	}
}

func (s *CrontabManager) metricsMutex(runCtx *gmap.Map, job *Job) {
	runCtx.Store("skipped", true)
}

func (s *CrontabManager) mutexFunc(job *Job) {
	f := job.Executor
	var f0 func(runCtx *gmap.Map)
	if !job.Mutex {
		f0 = func(runCtx *gmap.Map) {
			f()
		}
	} else {
		var lock = new(int32)
		f0 = func(runCtx *gmap.Map) {
			if !atomic.CompareAndSwapInt32(lock, 0, 1) {
				//pre task is running, skip this round.
				//fmt.Println("skipped")
				s.metricsMutex(runCtx, job)
				return
			}
			defer func() {
				atomic.StoreInt32(lock, 0)
			}()
			f()
		}
	}
	job.Executor = func() {
		runCtx := gmap.New()
		defer func() {
			s.metricsEnd(runCtx, job)
		}()
		s.metricsStart(runCtx, job)
		f0(runCtx)
	}
}

func (s *CrontabManager) AddJob(job Job) (jobID int, err error) {
	s.l.Lock()
	defer s.l.Unlock()
	s.initJob(&job)
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
	s.initTime = time.Now()
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
	if !job.triggerAt.IsZero() {
		triggerAt = job.triggerAt.Unix()
	}
	return &JobItem{
		JobID:        int(job.entryID),
		CronExp:      job.CronExp,
		Description:  job.Description,
		PrevAt:       prev,
		NextAt:       entry.Next.Unix(),
		TriggerAt:    triggerAt,
		RunningCount: int64(atomic.LoadInt32(job.runningCount)),
		TriggerCount: atomic.LoadUint64(job.triggerCount),
		Mutex:        job.Mutex,
		RawJob:       job,
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
	job.(*Job).triggerAt = time.Now()
	go func() {
		defer gerror.Recover(func(e interface{}) {
			err = fmt.Errorf("%s", gerror.Wrap(e).ErrorStack())
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
		switch v := gfile.BaseName(r.URL.Path); v {
		case "joblist":
			data, err = s.handleJobList(ctx)
		case "joblist.json":
			data, err = s.handleJobListJson(ctx)
		case "triggerjob":
			data, err = s.handleTriggerJob(ctx)
		case "history":
			data, err = s.handleHistory(ctx)
		default:
			if strings.HasSuffix(filepath.Dir(r.URL.Path), "/static") {
				b, e := static.ReadFile("static/" + v)
				if e != nil {
					ctx.Write(httpData(500, nil, e.Error()))
					return
				}
				ext := filepath.Ext(v)
				typ := mime.TypeByExtension(ext)
				cacheSince := time.Now().Format(http.TimeFormat)
				cacheUntil := time.Now().AddDate(66, 0, 0).Format(http.TimeFormat)
				w.Header().Set("Cache-Control", "max-age:290304000, public")
				w.Header().Set("Last-Modified", cacheSince)
				w.Header().Set("Expires", cacheUntil)
				w.Header().Set("Content-Type", typ)
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(b)))
				ctx.Write(b)
			}
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
func (s *CrontabManager) init() {
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
	s.c = c
	dir, _ := templates.ReadDir("template")
	bindData := map[string]string{}
	for _, v := range dir {
		if !v.IsDir() && filepath.Ext(v.Name()) == templateExt {
			b, _ := templates.ReadFile("template/" + v.Name())
			bindData[gfile.FileName(v.Name())] = base64.StdEncoding.EncodeToString(b)
		}
	}
	gtemplate.SetBinBase64(bindData)
	var err error
	s.tpl, err = gtemplate.NewTemplate(gctx.NewCtx(), "")
	if err != nil {
		glog.Warnf("new template fail, error: %s", err)
		return
	}
	s.tpl.Extension(".gohtml")
	s.tpl.Funcs(map[string]interface{}{
		"date1": func(args ...interface{}) (interface{}, error) {
			t := gcast.ToString(args[1])
			if t == "0" {
				return args[2], nil
			}
			return time.Unix(gcast.ToInt64(t), 0).Format(args[0].(string)), nil
		},
		"tojs": func(str string) string {
			s, _ := base64.StdEncoding.DecodeString(str)
			return string(s)
		},
	})
	err = s.tpl.Parse()
	if err != nil {
		glog.Warnf("init crontab manager fail, error: %s", err)
		return
	}

}
func (s *CrontabManager) handleHistory(ctx gcore.Ctx) (b interface{}, err error) {
	jobID := gcast.ToInt(ctx.GET("jobid"))
	if jobID == 0 {
		err = fmt.Errorf("job id error")
		return
	}
	job := s.GetJob(jobID)
	if job == nil {
		err = fmt.Errorf("job %d not exists", jobID)
		return
	}
	tplData := map[string]interface{}{
		"job": job,
	}
	runData := job.RawJob.MetricsRunData()
	historyMap := map[int64]map[string]interface{}{}
	sortBy := []int64{}

	for _, v := range runData {
		key := v.StartAt.UnixNano()
		sortBy = append(sortBy, key)
		dur_str := "0ms"
		dur_ms := v.EndAt.Sub(v.StartAt).Milliseconds()
		if dur_ms > 0 {
			dur_str = gcast.ToString(v.EndAt.Sub(v.StartAt).Round(time.Millisecond))
		}
		historyMap[key] = map[string]interface{}{
			"start_at": v.StartAt.UnixMilli(),
			"dur_ms":   dur_ms,
			"skipped":  v.Skipped,
			"dur_str":  dur_str,
		}
	}
	sort.Slice(sortBy, func(i, j int) bool {
		return sortBy[i] > sortBy[j]
	})
	history := []map[string]interface{}{}
	for _, k := range sortBy {
		history = append(history, historyMap[k])
	}
	h, _ := json.Marshal(history)
	j, _ := json.Marshal(job)
	tplData["data"] = string(h)
	tplData["jobJson"] = string(j)
	tplData["job"] = job.RawJob
	d, err := s.tpl.Execute("history", tplData)
	if err != nil {
		return nil, err
	}
	ctx.Write(d)
	return
}

func (s *CrontabManager) handleJobList(ctx gcore.Ctx) (b interface{}, err error) {
	tplData := map[string]interface{}{
		"rows":          s.JobList(),
		"init_time":     s.initTime.In(time.Local).Format("2006-01-02 15:04:05"),
		"init_time_dur": gcast.ToString(time.Now().Sub(s.initTime).Round(time.Second)),
	}
	d, err := s.tpl.Execute("index", tplData)
	if err != nil {
		return nil, err
	}
	ctx.Write(d)
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
	m := &CrontabManager{
		jobs: gmap.New(),
	}
	m.init()
	return m
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
