package validators

import (
	"errors"
	"sync"

	"github.com/gate4ai/gate4ai/shared"
	"golang.org/x/time/rate"
)

// Throttling limits the rate of messages per session using RPM (requests per minute) and RPS (requests per second)
type Throttling struct {
	// Default values to use if not specified in session parameters
	defaultRPM int
	defaultRPS int
	mu         sync.RWMutex
}

// Constants for session parameter keys
const (
	RPMParamKey      = "throttling_rpm"
	RPSParamKey      = "throttling_rps"
	LimitersParamKey = "throttling_limiters"
)

// limiterPair holds the RPS and RPM limiters for a session
type limiterPair struct {
	rpsLimiter *rate.Limiter
	rpmLimiter *rate.Limiter
}

// NewThrottling creates a new throttling validator
func NewThrottling(defaultRPS, defaultRPM int) *Throttling {
	return &Throttling{
		defaultRPM: defaultRPM,
		defaultRPS: defaultRPS,
	}
}

// getLimiters gets or creates rate limiters for a session
func (t *Throttling) getLimiters(session shared.ISession) *limiterPair {
	sessionParams := session.GetParams()

	t.mu.RLock()
	// Get RPM and RPS values from session parameters
	rpm := t.defaultRPM
	rps := t.defaultRPS
	defer t.mu.RUnlock()

	// Check if custom RPM is specified in the session
	if rpmValue, ok := sessionParams.Load(RPMParamKey); ok {
		if rpmInt, ok := rpmValue.(int); ok && rpmInt > 0 {
			rpm = rpmInt
		}
	}

	// Check if custom RPS is specified in the session
	if rpsValue, ok := sessionParams.Load(RPSParamKey); ok {
		if rpsInt, ok := rpsValue.(int); ok && rpsInt > 0 {
			rps = rpsInt
		}
	}

	// Check if limiters already exist in session
	value, ok := sessionParams.Load(LimitersParamKey)
	pair, ok2 := value.(*limiterPair)
	if ok && ok2 && pair != nil {
		return pair
	}

	// Create new limiters
	pair = &limiterPair{}

	if rpm > 0 {
		// Convert RPM to requests per second for the limiter
		pair.rpmLimiter = rate.NewLimiter(rate.Limit(rpm)/60.0, rpm)
	}

	if rps > 0 {
		pair.rpsLimiter = rate.NewLimiter(rate.Limit(rps), rps)
	}

	sessionParams.Store(LimitersParamKey, pair)
	return pair
}

// Validate implements the MessageValidator interface
func (t *Throttling) Validate(msg *shared.Message) error {
	// Get limiters for this session
	pair := t.getLimiters(msg.Session)

	// Check RPM limit
	if !pair.rpmLimiter.Allow() {
		return errors.New("RPM throttling limit exceeded")
	}

	// Check RPS limit
	if !pair.rpsLimiter.Allow() {
		return errors.New("RPS throttling limit exceeded")
	}

	return nil
}
