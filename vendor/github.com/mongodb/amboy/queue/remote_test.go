package queue

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/job"
	"github.com/mongodb/amboy/pool"
	"github.com/mongodb/grip"
	"github.com/satori/go.uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"gopkg.in/mgo.v2"
)

func init() {
	job.RegisterDefaultJobs()
}

type RemoteUnorderedSuite struct {
	queue             *remoteUnordered
	driver            Driver
	driverConstructor func() Driver
	tearDown          func()
	require           *require.Assertions
	canceler          context.CancelFunc
	suite.Suite
}

func TestRemoteUnorderedInternalDriverSuite(t *testing.T) {
	if runtime.Compiler == "gccgo" {
		t.Skip("gccgo not supported.")
	}

	tests := new(RemoteUnorderedSuite)
	tests.driverConstructor = func() Driver {
		return NewInternalDriver()
	}
	tests.tearDown = func() {}

	suite.Run(t, tests)
}

func TestRemoteUnorderedPriorityDriverSuite(t *testing.T) {
	if runtime.Compiler == "gccgo" {
		t.Skip("gccgo not supported.")
	}

	tests := new(RemoteUnorderedSuite)
	tests.driverConstructor = func() Driver {
		return NewPriorityDriver()
	}
	tests.tearDown = func() {}

	suite.Run(t, tests)
}

func TestRemoteUnorderedMgoSuite(t *testing.T) {
	tests := new(RemoteUnorderedSuite)
	name := "test-" + uuid.NewV4().String()
	uri := "mongodb://localhost"
	opts := DefaultMongoDBOptions()
	opts.DB = "amboy_test"
	tests.driverConstructor = func() Driver {
		return NewMgoDriver(name, opts)
	}

	tests.tearDown = func() {
		session, err := mgo.Dial(uri)
		defer session.Close()
		if err != nil {
			grip.Error(err)
			return
		}

		err = session.DB(opts.DB).DropDatabase()
		if err != nil {
			grip.Error(err)
			return
		}
	}

	suite.Run(t, tests)
}

// TODO run these same tests with different drivers by cloning the
// above Test function and replacing the driverConstructor function.

func (s *RemoteUnorderedSuite) SetupSuite() {
	s.require = s.Require()
}

func (s *RemoteUnorderedSuite) SetupTest() {
	ctx, canceler := context.WithCancel(context.Background())
	s.driver = s.driverConstructor()
	s.canceler = canceler
	s.NoError(s.driver.Open(ctx))
	s.queue = NewRemoteUnordered(2).(*remoteUnordered)
}

func (s *RemoteUnorderedSuite) TearDownTest() {
	// this order is important, running teardown before canceling
	// the context to prevent closing the connection before
	// running the teardown procedure, given that some connection
	// resources may be shared in the driver.
	s.tearDown()
	s.canceler()
}

func (s *RemoteUnorderedSuite) TestDriverIsUnitializedByDefault() {
	s.Nil(s.queue.Driver())
}

func (s *RemoteUnorderedSuite) TestRemoteUnorderdImplementsQueueInterface() {
	s.Implements((*amboy.Queue)(nil), s.queue)
}

func (s *RemoteUnorderedSuite) TestJobPutIntoQueueFetchableViaGetMethod() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.NoError(s.queue.SetDriver(s.driver))
	s.NotNil(s.queue.Driver())

	j := job.NewShellJob("echo foo", "")
	name := j.ID()
	s.NoError(s.queue.Put(ctx, j))
	fetchedJob, ok := s.queue.Get(ctx, name)

	if s.True(ok) {
		s.IsType(j.Dependency(), fetchedJob.Dependency())
		s.Equal(j.ID(), fetchedJob.ID())
		s.Equal(j.Type(), fetchedJob.Type())

		nj := fetchedJob.(*job.ShellJob)
		s.Equal(j.ID(), nj.ID())
		s.Equal(j.Status().Completed, nj.Status().Completed)
		s.Equal(j.Command, nj.Command, fmt.Sprintf("%+v\n%+v", j, nj))
		s.Equal(j.Output, nj.Output)
		s.Equal(j.WorkingDir, nj.WorkingDir)
		s.Equal(j.Type(), nj.Type())
	}
}

func (s *RemoteUnorderedSuite) TestJobsDoNotCompleteWithCanceledQueueContext() {
	s.NoError(s.queue.SetDriver(s.driver))
	s.NotNil(s.queue.Driver())

	ctx, cancel := context.WithCancel(context.Background())
	s.NoError(s.queue.Start(ctx))

	j1 := job.NewShellJob("echo foo", "")
	name := j1.ID()
	s.NoError(s.queue.Put(ctx, j1))
	amboy.WaitInterval(ctx, s.queue, 10*time.Millisecond)
	fetchedJob, ok := s.queue.Get(ctx, name)
	nj := fetchedJob.(*job.ShellJob)
	s.True(ok)
	s.True(nj.Status().Completed, "before canceling the context, a job will complete")

	cancel()

	j2 := job.NewShellJob("echo foo", "")
	name = j2.ID()
	s.NoError(s.queue.Put(ctx, j2))
	amboy.WaitInterval(ctx, s.queue, 10*time.Millisecond)
	fetchedJob, ok = s.queue.Get(ctx, name)
	nj = fetchedJob.(*job.ShellJob)
	s.True(ok)
	s.False(nj.Status().Completed, "after canceling the context, a job will not complete")
}

func (s *RemoteUnorderedSuite) TestGetMethodHandlesMissingJobs() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Nil(s.queue.Driver())
	s.NoError(s.queue.SetDriver(s.driver))
	s.NotNil(s.queue.Driver())

	s.NoError(s.queue.Start(ctx))

	j := job.NewShellJob("echo foo", "")
	name := j.ID()

	// before putting a job in the queue, it shouldn't exist.
	fetchedJob, ok := s.queue.Get(ctx, name)
	s.False(ok)
	s.Nil(fetchedJob)

	s.NoError(s.queue.Put(ctx, j))

	// wrong name also returns error case
	fetchedJob, ok = s.queue.Get(ctx, name+name)
	s.False(ok)
	s.Nil(fetchedJob)
}

func (s *RemoteUnorderedSuite) TestInternalRunnerCanBeChangedBeforeStartingTheQueue() {
	s.NoError(s.queue.SetDriver(s.driver))

	originalRunner := s.queue.Runner()
	newRunner := pool.NewLocalWorkers(3, s.queue)
	s.NotEqual(originalRunner, newRunner)

	s.NoError(s.queue.SetRunner(newRunner))
	s.Exactly(newRunner, s.queue.Runner())
}

func (s *RemoteUnorderedSuite) TestInternalRunnerCannotBeChangedAfterStartingAQueue() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.NoError(s.queue.SetDriver(s.driver))

	runner := s.queue.Runner()
	s.False(s.queue.Started())
	s.NoError(s.queue.Start(ctx))
	s.True(s.queue.Started())

	newRunner := pool.NewLocalWorkers(2, s.queue)
	s.Error(s.queue.SetRunner(newRunner))
	s.NotEqual(runner, newRunner)
}

func (s *RemoteUnorderedSuite) TestPuttingAJobIntoAQueueImpactsStats() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.NoError(s.queue.SetDriver(s.driver))

	existing := s.queue.Stats(ctx)
	s.NoError(s.queue.Start(ctx))

	j := job.NewShellJob("true", "")
	s.NoError(s.queue.Put(ctx, j))

	_, ok := s.queue.Get(ctx, j.ID())
	s.True(ok)

	stats := s.queue.Stats(ctx)

	report := fmt.Sprintf("%+v", stats)
	s.Equal(existing.Total+1, stats.Total, report)
}

func (s *RemoteUnorderedSuite) TestQueueFailsToStartIfDriverIsNotSet() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Nil(s.queue.driver)
	s.Nil(s.queue.Driver())
	s.Error(s.queue.Start(ctx))

	s.NoError(s.queue.SetDriver(s.driver))

	s.NotNil(s.queue.driver)
	s.NotNil(s.queue.Driver())
	s.NoError(s.queue.Start(ctx))
}

func (s *RemoteUnorderedSuite) TestQueueFailsToStartIfRunnerIsNotSet() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.NotNil(s.queue.Runner())

	s.NoError(s.queue.SetRunner(nil))

	s.Nil(s.queue.runner)
	s.Nil(s.queue.Runner())

	s.Error(s.queue.Start(ctx))
}

func (s *RemoteUnorderedSuite) TestSetDriverErrorsIfQueueHasStarted() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.NoError(s.queue.SetDriver(s.driver))
	s.NoError(s.queue.Start(ctx))

	s.Error(s.queue.SetDriver(s.driver))
}

func (s *RemoteUnorderedSuite) TestStartMethodCanBeCalledMultipleTimes() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.NoError(s.queue.SetDriver(s.driver))
	for i := 0; i < 200; i++ {
		s.NoError(s.queue.Start(ctx))
		s.True(s.queue.Started())
	}
}

func (s *RemoteUnorderedSuite) TestNextMethodSkipsLockedJobs() {
	s.require.NoError(s.queue.SetDriver(s.driver))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	numLocked := 0
	lockedJobs := map[string]struct{}{}

	created := 0
	for i := 0; i < 30; i++ {
		cmd := fmt.Sprintf("echo 'foo: %d'", i)
		j := job.NewShellJob(cmd, "")

		if i%3 == 0 {
			numLocked++
			err := j.Lock(s.driver.ID())
			s.NoError(err)

			s.Error(j.Lock("elsewhere"))
			lockedJobs[j.ID()] = struct{}{}
		}

		if s.NoError(s.queue.Put(ctx, j)) {
			created++
		}
	}

	s.queue.started = true
	s.require.NoError(s.queue.Start(ctx))
	go s.queue.jobServer(ctx)

	observed := 0
checkResults:
	for {
		select {
		case <-ctx.Done():
			break checkResults
		default:
			work := s.queue.Next(ctx)
			if work == nil {
				continue checkResults
			}
			observed++

			_, ok := lockedJobs[work.ID()]
			s.False(ok, fmt.Sprintf("%s\n\tjob: %+v\n\tqueue: %+v",
				work.ID(), work.Status(), s.queue.Stats(ctx)))

			if observed == created || observed+numLocked == created {
				break checkResults
			}
		}
	}
	qStat := s.queue.Stats(ctx)
	s.True(qStat.Running >= numLocked)
	s.True(qStat.Total == created)
	s.True(qStat.Completed <= observed, fmt.Sprintf("%d <= %d", qStat.Completed, observed))
	s.Equal(numLocked, qStat.Running)
}

func (s *RemoteUnorderedSuite) TestJobStatsIterator() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.require.NoError(s.queue.SetDriver(s.driver))

	names := make(map[string]struct{})

	for i := 0; i < 30; i++ {
		cmd := fmt.Sprintf("echo 'foo: %d'", i)
		j := job.NewShellJob(cmd, "")

		s.NoError(s.queue.Put(ctx, j))
		names[j.ID()] = struct{}{}
	}

	counter := 0
	for stat := range s.queue.JobStats(ctx) {
		_, ok := names[stat.ID]
		s.True(ok)
		counter++
	}
	s.Equal(len(names), counter)
	s.Equal(counter, 30)
}

func (s *RemoteUnorderedSuite) TestTimeInfoPersists() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.require.NoError(s.queue.SetDriver(s.driver))
	j := newMockJob()
	s.Zero(j.TimeInfo())
	s.NoError(s.queue.Put(ctx, j))
	go s.queue.jobServer(ctx)
	j2 := s.queue.Next(ctx)
	s.NotZero(j2.TimeInfo())
}
