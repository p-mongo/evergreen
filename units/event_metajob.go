package units

import (
	"context"
	"fmt"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model/event"
	"github.com/evergreen-ci/evergreen/model/notification"
	"github.com/evergreen-ci/evergreen/model/trigger"
	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/dependency"
	"github.com/mongodb/amboy/job"
	"github.com/mongodb/amboy/registry"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/mongodb/grip/sometimes"
	"github.com/pkg/errors"
)

const (
	eventMetaJobName = "event-metajob"
)

func init() {
	registry.AddJobType(eventMetaJobName, func() amboy.Job { return makeEventMetaJob() })
}

func notificationIsEnabled(flags *evergreen.ServiceFlags, n *notification.Notification) bool {
	switch n.Subscriber.Type {
	case event.GithubPullRequestSubscriberType:
		return !flags.GithubStatusAPIDisabled

	case event.JIRAIssueSubscriberType, event.JIRACommentSubscriberType:
		return !flags.JIRANotificationsDisabled

	case event.EvergreenWebhookSubscriberType:
		return !flags.WebhookNotificationsDisabled

	case event.EmailSubscriberType:
		return !flags.EmailNotificationsDisabled

	case event.SlackSubscriberType:
		return !flags.SlackNotificationsDisabled

	default:
		grip.Alert(message.Fields{
			"message": "notificationIsEnabled saw unknown subscriber type",
			"cause":   "programmer error",
		})
	}

	return false
}

type eventMetaJob struct {
	job.Base `bson:"job_base" json:"job_base" yaml:"job_base"`
	q        amboy.Queue
	events   []event.EventLogEntry
	flags    *evergreen.ServiceFlags
}

func makeEventMetaJob() *eventMetaJob {
	j := &eventMetaJob{
		Base: job.Base{
			JobType: amboy.JobType{
				Name:    eventMetaJobName,
				Version: 0,
			},
		},
	}
	j.SetDependency(dependency.NewAlways())
	j.SetPriority(1)

	return j
}

func NewEventMetaJob(q amboy.Queue, ts string) amboy.Job {
	j := makeEventMetaJob()
	j.q = q

	j.SetID(fmt.Sprintf("%s:%s", eventMetaJobName, ts))

	return j
}

func tryProcessOneEvent(e *event.EventLogEntry) (n []notification.Notification, err error) {
	if e == nil {
		return nil, errors.New("nil event")
	}

	defer func() {
		if r := recover(); r != nil {
			n = nil
			err = errors.Errorf("panicked while processing event %s", e.ID.Hex())
			grip.Alert(message.WrapError(err, message.Fields{
				"job":         eventMetaJobName,
				"source":      "events-processing",
				"event_id":    e.ID.Hex(),
				"event_type":  e.ResourceType,
				"panic_value": r,
			}))
		}
	}()

	n, err = trigger.NotificationsFromEvent(e)
	grip.Error(message.WrapError(err, message.Fields{
		"job":        eventMetaJobName,
		"source":     "events-processing",
		"message":    "errors processing triggers for event",
		"event_id":   e.ID.Hex(),
		"event_type": e.ResourceType,
	}))

	return n, err
}

func (j *eventMetaJob) dispatchLoop(ctx context.Context) error {
	// TODO: if this is a perf problem, it could be multithreaded. For now,
	// we just log time
	startTime := time.Now()
	bulk, err := notification.BulkInserter(ctx)
	if err != nil {
		return errors.WithStack(err)
	}

	logger := event.NewDBEventLogger(event.AllLogCollection)
	catcher := grip.NewSimpleCatcher()
	notifications := make([][]notification.Notification, len(j.events))

	for i := range j.events {
		notifications[i], err = tryProcessOneEvent(&j.events[i])
		catcher.Add(err)

		for _, n := range notifications[i] {
			catcher.Add(bulk.Append(n))
		}

	}
	catcher.Add(bulk.Close())

	for idx := range notifications {
		catcher.Add(j.dispatch(notifications[idx]))
		catcher.Add(logger.MarkProcessed(&j.events[idx]))
	}

	endTime := time.Now()
	totalDuration := endTime.Sub(startTime)

	grip.Info(message.Fields{
		"job_id":     j.ID(),
		"job":        eventMetaJobName,
		"source":     "events-processing",
		"message":    "stats",
		"start_time": startTime.String(),
		"end_time":   endTime.String(),
		"duration":   totalDuration.String(),
		"n":          len(j.events),
	})

	return catcher.Resolve()
}

func (j *eventMetaJob) dispatch(notifications []notification.Notification) error {
	catcher := grip.NewSimpleCatcher()
	for i := range notifications {
		if notificationIsEnabled(j.flags, &notifications[i]) {
			catcher.Add(j.q.Put(newEventNotificationJob(notifications[i].ID)))
		} else {
			catcher.Add(notifications[i].MarkError(errors.New("sender disabled")))
		}
	}

	return catcher.Resolve()
}

func (j *eventMetaJob) Run(ctx context.Context) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()
	defer j.MarkComplete()

	if j.q == nil {
		env := evergreen.GetEnvironment()
		j.q = env.RemoteQueue()
	}
	if j.q == nil || !j.q.Started() {
		j.AddError(errors.New("evergreen environment not setup correctly"))
		return
	}

	var err error
	j.flags, err = evergreen.GetServiceFlags()
	if err != nil {
		j.AddError(errors.Wrap(err, "error retrieving admin settings"))
		return
	}
	if j.flags.EventProcessingDisabled {
		grip.InfoWhen(sometimes.Percent(evergreen.DegradedLoggingPercent), message.Fields{
			"job":     eventMetaJobName,
			"message": "events processing is disabled",
		})
		return
	}

	j.events, err = event.FindUnprocessedEvents()
	if err != nil {
		j.AddError(err)
		return
	}

	if len(j.events) == 0 {
		grip.Info(message.Fields{
			"job_id":  j.ID(),
			"job":     eventMetaJobName,
			"time":    time.Now().String(),
			"message": "no events need to be processed",
			"source":  "events-processing",
		})
		return
	}

	j.AddError(j.dispatchLoop(ctx))
}
