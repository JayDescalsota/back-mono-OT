package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/clinicmanager/services/booking/db"
	"github.com/clinicmanager/services/booking/graph/model"
	"github.com/clinicmanager/services/booking/repository"
	"github.com/clinicmanager/shared/cache"
	sharedCtx "github.com/clinicmanager/shared/context"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var dayToRRULE = map[int]string{
	0: "MO", 1: "TU", 2: "WE", 3: "TH", 4: "FR", 5: "SA", 6: "SU",
}

var rruleToDay = map[string]int{
	"MO": 0, "TU": 1, "WE": 2, "TH": 3, "FR": 4, "SA": 5, "SU": 6,
}

type BookingService struct {
	BookingRepository *repository.BookingRepository
	Cache             *redis.Client
	// Payments is set in wiring (monolith) to enforce prepaid credits.
	// Nil disables money checks (e.g. in unit tests).
	Payments FundsLedger
	// BranchTimezone resolves a branch's IANA timezone so branch hours are
	// read as wall-clock times. Nil falls back to UTC.
	BranchTimezone func(ctx context.Context, branchID string) (string, error)
}

// FundsLedger is the subset of the payment service that scheduling needs.
// Declared here (like userservice.Mailer) so booking doesn't import payment.
type FundsLedger interface {
	GetBranchPrice(ctx context.Context, branchID, appointmentTypeID string) (int, bool, error)
	GetBranchPaymentMode(ctx context.Context, branchID string) (string, error)
	HoldFunds(ctx context.Context, tenantID, branchID, patientID, appointmentID string, amountCents int) error
	CaptureHold(ctx context.Context, tenantID, branchID, patientID, appointmentID string, amountCents int) error
	ConsumeForAppointment(ctx context.Context, tenantID, branchID, patientID, appointmentID string, amountCents int) error
	ReleaseHold(ctx context.Context, appointmentID string) error
}

func NewBookingService(bookingRepository *repository.BookingRepository, cacheClient *redis.Client) *BookingService {
	return &BookingService{
		BookingRepository: bookingRepository,
		Cache:             cacheClient,
	}
}

// Cache helpers

func (s *BookingService) cacheGet(ctx context.Context, key string, dest interface{}) (bool, error) {
	if s.Cache == nil {
		return false, nil
	}
	val, err := s.Cache.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(val, dest); err != nil {
		return false, err
	}
	return true, nil
}

func (s *BookingService) cacheSet(ctx context.Context, key string, val interface{}) error {
	if s.Cache == nil {
		return nil
	}
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return s.Cache.Set(ctx, key, data, cache.DefaultTTL).Err()
}

func (s *BookingService) cacheDel(ctx context.Context, keys ...string) {
	if s.Cache == nil {
		return
	}
	s.Cache.Del(ctx, keys...)
}

// BranchHours

func (s *BookingService) GetBranchHoursByID(ctx context.Context, id string) (*db.BunBranchHours, error) {
	ck := cache.Key("booking", "branch_hours", id)
	var cached db.BunBranchHours
	if ok, _ := s.cacheGet(ctx, ck, &cached); ok {
		return &cached, nil
	}
	m, err := s.BookingRepository.FindBranchHoursByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.cacheSet(ctx, ck, m)
	return m, nil
}

func (s *BookingService) GetBranchHoursByBranch(ctx context.Context, branchID string) ([]*db.BunBranchHours, error) {
	return s.BookingRepository.FindBranchHoursByBranch(ctx, branchID)
}

func (s *BookingService) ListBranchHours(ctx context.Context) ([]*db.BunBranchHours, error) {
	return s.BookingRepository.ListBranchHours(ctx)
}

func (s *BookingService) ListAppointmentTypes(ctx context.Context) ([]*db.BunAppointmentType, error) {
	return s.BookingRepository.ListAppointmentTypes(ctx)
}

func (s *BookingService) ListTherapyDisciplines(ctx context.Context) ([]*db.BunTherapyDiscipline, error) {
	return s.BookingRepository.ListTherapyDisciplines(ctx)
}

func (s *BookingService) ListDisciplinesByType(ctx context.Context, appointmentTypeID string) ([]*db.BunTherapyDiscipline, error) {
	return s.BookingRepository.ListDisciplinesByType(ctx, appointmentTypeID)
}

func (s *BookingService) CreateBranchHours(ctx context.Context, input model.BranchHoursInput) (*db.BunBranchHours, error) {
	tctx := sharedCtx.FromContext(ctx)
	now := time.Now()

	m := &db.BunBranchHours{
		ID:             uuid.NewString(),
		TenantID:       tctx.TenantID,
		BranchID:       tctx.BranchID,
		RecurrenceRule: rruleFromDay(input.DayOfWeek),
		OpenTime:       parseTimeVal(ptrStr(input.OpenTime)),
		CloseTime:      parseTimeVal(ptrStr(input.CloseTime)),
		EffectiveFrom:  time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		IsActive:       true,
		CreatedAt:      now,
		UpdatedAt:      now,
		CreatedAction:  "CREATE",
		UpdatedAction:  "CREATE",
	}
	if err := s.BookingRepository.CreateBranchHours(ctx, m); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "branch_hours", m.ID))
	return m, nil
}

func (s *BookingService) UpdateBranchHours(ctx context.Context, id string, input model.BranchHoursUpdateInput) (*db.BunBranchHours, error) {
	existing, err := s.BookingRepository.FindBranchHoursByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if input.DayOfWeek != nil {
		existing.RecurrenceRule = rruleFromDay(*input.DayOfWeek)
	}
	if input.OpenTime != nil {
		existing.OpenTime = parseTimeVal(*input.OpenTime)
	}
	if input.CloseTime != nil {
		existing.CloseTime = parseTimeVal(*input.CloseTime)
	}
	if input.IsActive != nil {
		existing.IsActive = *input.IsActive
	}
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "UPDATE"
	if err := s.BookingRepository.UpdateBranchHours(ctx, existing); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "branch_hours", id))
	return existing, nil
}

func (s *BookingService) DeleteBranchHours(ctx context.Context, id string) (bool, error) {
	existing, err := s.BookingRepository.FindBranchHoursByID(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	existing.IsActive = false
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "DELETE"
	if err := s.BookingRepository.UpdateBranchHours(ctx, existing); err != nil {
		return false, err
	}
	s.cacheDel(ctx, cache.Key("booking", "branch_hours", id))
	return true, nil
}

// Branch special hours (date overrides for holidays / special openings)

func (s *BookingService) GetSpecialHoursByBranch(ctx context.Context, branchID string) ([]*db.BunBranchSpecialHours, error) {
	return s.BookingRepository.FindSpecialHoursByBranch(ctx, branchID)
}

func (s *BookingService) CreateSpecialHours(ctx context.Context, input model.BranchSpecialHoursInput) (*db.BunBranchSpecialHours, error) {
	tctx := sharedCtx.FromContext(ctx)
	if tctx.TenantID == "" {
		return nil, fmt.Errorf("missing tenant context")
	}
	date, err := resolveSpecialDate(input)
	if err != nil {
		return nil, err
	}
	if !input.IsClosed {
		if input.OpenTime == nil || input.CloseTime == nil {
			return nil, fmt.Errorf("set both an open and a close time, or mark the day as closed")
		}
		if !parseTimeVal(*input.OpenTime).Before(parseTimeVal(*input.CloseTime)) {
			return nil, fmt.Errorf("closing time must be after opening time")
		}
	}

	now := time.Now()
	m := &db.BunBranchSpecialHours{
		ID:            uuid.NewString(),
		TenantID:      tctx.TenantID,
		BranchID:      tctx.BranchID,
		Name:          strings.TrimSpace(input.Name),
		IsYearly:      input.IsYearly,
		SpecialDate:   date,
		IsClosed:      input.IsClosed,
		IsActive:      true,
		CreatedAt:     now,
		UpdatedAt:     now,
		CreatedAction: "CREATE",
		UpdatedAction: "CREATE",
	}
	if !input.IsClosed {
		open := parseTimeVal(*input.OpenTime)
		close := parseTimeVal(*input.CloseTime)
		m.OpenTime = &open
		m.CloseTime = &close
	}
	if err := s.BookingRepository.CreateSpecialHours(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *BookingService) UpdateSpecialHours(ctx context.Context, id string, input model.BranchSpecialHoursInput) (*db.BunBranchSpecialHours, error) {
	existing, err := s.BookingRepository.FindSpecialHoursByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("special schedule not found")
	}
	date, err := resolveSpecialDate(input)
	if err != nil {
		return nil, err
	}
	if !input.IsClosed {
		if input.OpenTime == nil || input.CloseTime == nil {
			return nil, fmt.Errorf("set both an open and a close time, or mark the day as closed")
		}
		if !parseTimeVal(*input.OpenTime).Before(parseTimeVal(*input.CloseTime)) {
			return nil, fmt.Errorf("closing time must be after opening time")
		}
	}

	existing.Name = strings.TrimSpace(input.Name)
	existing.IsYearly = input.IsYearly
	existing.SpecialDate = date
	existing.IsClosed = input.IsClosed
	if input.IsClosed {
		existing.OpenTime = nil
		existing.CloseTime = nil
	} else {
		open := parseTimeVal(*input.OpenTime)
		close := parseTimeVal(*input.CloseTime)
		existing.OpenTime = &open
		existing.CloseTime = &close
	}
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "UPDATE"
	if err := s.BookingRepository.UpdateSpecialHours(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *BookingService) DeleteSpecialHours(ctx context.Context, id string) (bool, error) {
	existing, err := s.BookingRepository.FindSpecialHoursByID(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	existing.IsActive = false
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "DELETE"
	if err := s.BookingRepository.UpdateSpecialHours(ctx, existing); err != nil {
		return false, err
	}
	return true, nil
}

// resolveSpecialDate turns a month/day pair into a stored date. Yearly
// entries use a leap year (2000) as a stable placeholder so Feb 29 is valid;
// one-off entries use the upcoming occurrence (this year, or next if past).
func resolveSpecialDate(input model.BranchSpecialHoursInput) (time.Time, error) {
	if input.Month < 1 || input.Month > 12 {
		return time.Time{}, fmt.Errorf("month must be between 1 and 12")
	}
	if input.Day < 1 || input.Day > 31 {
		return time.Time{}, fmt.Errorf("day must be between 1 and 31")
	}
	year := time.Now().UTC().Year()
	if input.IsYearly {
		year = 2000
	}
	date := time.Date(year, time.Month(input.Month), input.Day, 0, 0, 0, 0, time.UTC)
	if date.Month() != time.Month(input.Month) || date.Day() != input.Day {
		return time.Time{}, fmt.Errorf("that day does not exist in the selected month")
	}
	if !input.IsYearly {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		if date.Before(today) {
			date = date.AddDate(1, 0, 0)
		}
	}
	return date, nil
}

// specialHoursMatchDay reports whether a stored special schedule applies to
// the given date: yearly rows match month/day, one-off rows match the date.
func specialHoursMatchDay(row *db.BunBranchSpecialHours, date time.Time) bool {
	if row.IsYearly {
		return row.SpecialDate.Month() == date.Month() && row.SpecialDate.Day() == date.Day()
	}
	return row.SpecialDate.Format("2006-01-02") == date.Format("2006-01-02")
}

// Branch hours enforcement
//
// booking_branch_hours stores operating windows as wall-clock times (the date
// part is a placeholder). Appointments must fit entirely inside one of the
// branch's active windows for the local weekday. A date-specific override
// (booking_branch_special_hours) replaces the weekly window for that date.

var dayNames = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

// branchLocation resolves the branch's timezone, falling back to UTC.
func (s *BookingService) branchLocation(ctx context.Context, branchID string) *time.Location {
	if s.BranchTimezone == nil {
		return time.UTC
	}
	name, err := s.BranchTimezone(ctx, branchID)
	if err != nil || name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// wallClockMinutes reads the hour/minute stored for a branch-hours row. Rows
// are normalised to UTC wall-clock on write, so UTC is the reference here.
func wallClockMinutes(t time.Time) int {
	u := t.UTC()
	return u.Hour()*60 + u.Minute()
}

func formatMinutes(m int) string {
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// validateWithinBranchHours rejects appointments outside the branch's
// configured operating hours. Branches without active hours stay
// unrestricted, so existing data keeps working.
func (s *BookingService) validateWithinBranchHours(ctx context.Context, branchID string, startAt, endAt time.Time) error {
	if branchID == "" {
		return nil
	}

	loc := s.branchLocation(ctx, branchID)
	localStart := startAt.In(loc)
	localEnd := endAt.In(loc)
	if localStart.Year() != localEnd.Year() || localStart.YearDay() != localEnd.YearDay() {
		return fmt.Errorf("appointments must start and end on the same day within branch hours")
	}
	startMin := localStart.Hour()*60 + localStart.Minute()
	endMin := localEnd.Hour()*60 + localEnd.Minute()

	// A date-specific override (holiday / special opening) wins over the
	// weekly schedule for that day.
	specials, err := s.BookingRepository.FindSpecialHoursByBranch(ctx, branchID)
	if err != nil {
		return err
	}
	dateKey := localStart.Format("2006-01-02")
	for _, sp := range specials {
		if !specialHoursMatchDay(sp, localStart) {
			continue
		}
		if sp.IsClosed {
			return fmt.Errorf("branch is closed on %s (%s)", dateKey, sp.Name)
		}
		if sp.OpenTime == nil || sp.CloseTime == nil {
			return nil // special opening with no time limit
		}
		openMin := wallClockMinutes(*sp.OpenTime)
		closeMin := wallClockMinutes(*sp.CloseTime)
		if closeMin > openMin && startMin >= openMin && endMin <= closeMin {
			return nil
		}
		return fmt.Errorf("appointment %s-%s is outside special hours on %s (%s-%s)",
			formatMinutes(startMin), formatMinutes(endMin), dateKey,
			formatMinutes(openMin), formatMinutes(closeMin))
	}

	hours, err := s.BookingRepository.FindBranchHoursByBranch(ctx, branchID)
	if err != nil {
		return err
	}

	active := make([]*db.BunBranchHours, 0, len(hours))
	for _, h := range hours {
		if h.IsActive && !h.OpenTime.IsZero() && !h.CloseTime.IsZero() && h.RecurrenceRule != "" {
			active = append(active, h)
		}
	}
	if len(active) == 0 {
		return nil
	}

	day := (int(localStart.Weekday()) + 6) % 7 // Monday=0 … Sunday=6

	var windows []string
	for _, h := range active {
		if !ruleCoversDay(h.RecurrenceRule, day) {
			continue
		}
		openMin := wallClockMinutes(h.OpenTime)
		closeMin := wallClockMinutes(h.CloseTime)
		if closeMin <= openMin {
			continue
		}
		if startMin >= openMin && endMin <= closeMin {
			return nil
		}
		windows = append(windows, formatMinutes(openMin)+"-"+formatMinutes(closeMin))
	}

	if len(windows) == 0 {
		return fmt.Errorf("branch is closed on %s", dayNames[day])
	}
	return fmt.Errorf("appointment %s-%s is outside branch hours on %s (%s)",
		formatMinutes(startMin), formatMinutes(endMin), dayNames[day], strings.Join(windows, ", "))
}

// PractitionerBranch

func (s *BookingService) GetPractitionerBranchByID(ctx context.Context, id string) (*db.BunPractitionerBranch, error) {
	ck := cache.Key("booking", "practitioner_branch", id)
	var cached db.BunPractitionerBranch
	if ok, _ := s.cacheGet(ctx, ck, &cached); ok {
		return &cached, nil
	}
	m, err := s.BookingRepository.FindPractitionerBranchByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.cacheSet(ctx, ck, m)
	return m, nil
}

func (s *BookingService) GetPractitionerBranchesByBranch(ctx context.Context, branchID, role string) ([]*db.BunPractitionerBranch, error) {
	if role != "" {
		return s.BookingRepository.FindPractitionerBranchesByBranchAndRole(ctx, branchID, role)
	}
	return s.BookingRepository.FindPractitionerBranchesByBranch(ctx, branchID)
}

func (s *BookingService) ListPractitionerBranches(ctx context.Context) ([]*db.BunPractitionerBranch, error) {
	return s.BookingRepository.ListPractitionerBranches(ctx)
}

func (s *BookingService) CreatePractitionerBranch(ctx context.Context, input model.PractitionerBranchInput) (*db.BunPractitionerBranch, error) {
	tctx := sharedCtx.FromContext(ctx)
	now := time.Now()

	m := &db.BunPractitionerBranch{
		ID:             uuid.NewString(),
		TenantID:       tctx.TenantID,
		PractitionerID: input.PractitionerID,
		BranchID:       tctx.BranchID,
		Role:           input.Role,
		IsActive:       true,
		JoinedAt:       now,
		CreatedAt:      now,
		UpdatedAt:      now,
		CreatedAction:  "CREATE",
		UpdatedAction:  "CREATE",
	}
	if err := s.BookingRepository.CreatePractitionerBranch(ctx, m); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "practitioner_branch", m.ID))
	return m, nil
}

func (s *BookingService) DeletePractitionerBranch(ctx context.Context, id string) (bool, error) {
	existing, err := s.BookingRepository.FindPractitionerBranchByID(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	existing.IsActive = false
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "DELETE"
	if err := s.BookingRepository.UpdatePractitionerBranch(ctx, existing); err != nil {
		return false, err
	}
	s.cacheDel(ctx, cache.Key("booking", "practitioner_branch", id))
	return true, nil
}

// PractitionerAvailability

func (s *BookingService) GetPractitionerAvailabilityByID(ctx context.Context, id string) (*db.BunPractitionerAvailability, error) {
	ck := cache.Key("booking", "practitioner_availability", id)
	var cached db.BunPractitionerAvailability
	if ok, _ := s.cacheGet(ctx, ck, &cached); ok {
		return &cached, nil
	}
	m, err := s.BookingRepository.FindPractitionerAvailabilityByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.cacheSet(ctx, ck, m)
	return m, nil
}

func (s *BookingService) GetPractitionerAvailabilities(ctx context.Context, branchID, practitionerID *string) ([]*db.BunPractitionerAvailability, error) {
	if branchID != nil && *branchID != "" {
		return s.BookingRepository.FindPractitionerAvailabilitiesByBranch(ctx, *branchID)
	}
	if practitionerID != nil && *practitionerID != "" {
		return s.BookingRepository.FindPractitionerAvailabilitiesByPractitioner(ctx, *practitionerID)
	}
	return s.BookingRepository.ListPractitionerAvailabilities(ctx)
}

func (s *BookingService) CreatePractitionerAvailability(ctx context.Context, input model.PractitionerAvailabilityInput) (*db.BunPractitionerAvailability, error) {
	tctx := sharedCtx.FromContext(ctx)
	now := time.Now()

	m := &db.BunPractitionerAvailability{
		ID:            uuid.NewString(),
		TenantID:      tctx.TenantID,
		Status:        "ACTIVE",
		IsActive:      true,
		CreatedAt:     now,
		UpdatedAt:     now,
		CreatedAction: "CREATE",
		UpdatedAction: "CREATE",
	}
	m.PractitionerID = input.PractitionerID
	if t, err := time.Parse(time.RFC3339, input.StartTime); err == nil {
		m.AvailableFrom = t
	}
	if t, err := time.Parse(time.RFC3339, input.EndTime); err == nil {
		m.AvailableTo = t
	}
	effFrom := time.Date(m.AvailableFrom.Year(), m.AvailableFrom.Month(), m.AvailableFrom.Day(), 0, 0, 0, 0, time.UTC)
	m.EffectiveFrom = effFrom

	if err := s.BookingRepository.CreatePractitionerAvailability(ctx, m); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "practitioner_availability", m.ID))
	return m, nil
}

func (s *BookingService) DeletePractitionerAvailability(ctx context.Context, id string) (bool, error) {
	existing, err := s.BookingRepository.FindPractitionerAvailabilityByID(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	existing.IsActive = false
	existing.Status = "INACTIVE"
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "DELETE"
	if err := s.BookingRepository.UpdatePractitionerAvailability(ctx, existing); err != nil {
		return false, err
	}
	s.cacheDel(ctx, cache.Key("booking", "practitioner_availability", id))
	return true, nil
}

// ScheduleTemplate

func (s *BookingService) GetScheduleTemplateByID(ctx context.Context, id string) (*db.BunScheduleTemplate, error) {
	ck := cache.Key("booking", "schedule_template", id)
	var cached db.BunScheduleTemplate
	if ok, _ := s.cacheGet(ctx, ck, &cached); ok {
		return &cached, nil
	}
	m, err := s.BookingRepository.FindScheduleTemplateByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.cacheSet(ctx, ck, m)
	return m, nil
}

func (s *BookingService) GetScheduleTemplates(ctx context.Context, branchID, practitionerID *string) ([]*db.BunScheduleTemplate, error) {
	if branchID != nil && *branchID != "" {
		return s.BookingRepository.FindScheduleTemplatesByBranch(ctx, *branchID)
	}
	if practitionerID != nil && *practitionerID != "" {
		return s.BookingRepository.FindScheduleTemplatesByPractitioner(ctx, *practitionerID)
	}
	return s.BookingRepository.ListScheduleTemplates(ctx)
}

func (s *BookingService) CreateScheduleTemplate(ctx context.Context, input model.ScheduleTemplateInput) (*db.BunScheduleTemplate, error) {
	tctx := sharedCtx.FromContext(ctx)
	now := time.Now()

	var practitionerID *string
	if input.PractitionerID != "" {
		practitionerID = &input.PractitionerID
	}

	m := &db.BunScheduleTemplate{
		ID:             uuid.NewString(),
		TenantID:       tctx.TenantID,
		BranchID:       tctx.BranchID,
		PractitionerID: practitionerID,
		RecurrenceRule: rruleFromDay(input.DayOfWeek),
		SlotDuration:   input.SlotDurationMinutes,
		BufferDuration: 0,
		Capacity:       1,
		BookedCount:    0,
		Status:         "ACTIVE",
		IsActive:       true,
		CreatedAt:      now,
		UpdatedAt:      now,
		CreatedAction:  "CREATE",
		UpdatedAction:  "CREATE",
	}
	if t, err := time.Parse("15:04", input.StartTime); err == nil {
		m.StartTime = time.Date(2000, 1, 1, t.Hour(), t.Minute(), 0, 0, time.UTC)
	} else if t, err := time.Parse("15:04:05", input.StartTime); err == nil {
		m.StartTime = time.Date(2000, 1, 1, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	}
	if t, err := time.Parse("15:04", input.EndTime); err == nil {
		m.EndTime = time.Date(2000, 1, 1, t.Hour(), t.Minute(), 0, 0, time.UTC)
	} else if t, err := time.Parse("15:04:05", input.EndTime); err == nil {
		m.EndTime = time.Date(2000, 1, 1, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	}
	effFrom := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	m.EffectiveFrom = effFrom

	if err := s.BookingRepository.CreateScheduleTemplate(ctx, m); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "schedule_template", m.ID))
	return m, nil
}

func (s *BookingService) UpdateScheduleTemplate(ctx context.Context, id string, input model.ScheduleTemplateUpdateInput) (*db.BunScheduleTemplate, error) {
	existing, err := s.BookingRepository.FindScheduleTemplateByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if input.DayOfWeek != nil {
		existing.RecurrenceRule = rruleFromDay(*input.DayOfWeek)
	}
	if input.StartTime != nil {
		if t, err := time.Parse("15:04", *input.StartTime); err == nil {
			existing.StartTime = time.Date(2000, 1, 1, t.Hour(), t.Minute(), 0, 0, time.UTC)
		}
	}
	if input.EndTime != nil {
		if t, err := time.Parse("15:04", *input.EndTime); err == nil {
			existing.EndTime = time.Date(2000, 1, 1, t.Hour(), t.Minute(), 0, 0, time.UTC)
		}
	}
	if input.SlotDurationMinutes != nil {
		existing.SlotDuration = *input.SlotDurationMinutes
	}
	if input.IsActive != nil {
		existing.IsActive = *input.IsActive
	}
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "UPDATE"

	if err := s.BookingRepository.UpdateScheduleTemplate(ctx, existing); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "schedule_template", id))
	return existing, nil
}

func (s *BookingService) DeleteScheduleTemplate(ctx context.Context, id string) (bool, error) {
	existing, err := s.BookingRepository.FindScheduleTemplateByID(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	existing.IsActive = false
	existing.Status = "INACTIVE"
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "DELETE"
	if err := s.BookingRepository.UpdateScheduleTemplate(ctx, existing); err != nil {
		return false, err
	}
	s.cacheDel(ctx, cache.Key("booking", "schedule_template", id))
	return true, nil
}

// AppointmentSlot

func (s *BookingService) GetAppointmentSlotByID(ctx context.Context, id string) (*db.BunAppointmentSlot, error) {
	ck := cache.Key("booking", "appointment_slot", id)
	var cached db.BunAppointmentSlot
	if ok, _ := s.cacheGet(ctx, ck, &cached); ok {
		return &cached, nil
	}
	m, err := s.BookingRepository.FindAppointmentSlotByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.cacheSet(ctx, ck, m)
	return m, nil
}

func (s *BookingService) GetAppointmentSlots(ctx context.Context, filter model.AppointmentSlotsFilter) ([]*db.BunAppointmentSlot, error) {
	return s.BookingRepository.FindAppointmentSlotsByFilter(ctx, filter.BranchID, filter.PractitionerID, filter.SlotDate, filter.Status)
}

func (s *BookingService) GenerateSlots(ctx context.Context, branchID, practitionerID, date string) ([]*db.BunAppointmentSlot, error) {
	tctx := sharedCtx.FromContext(ctx)
	now := time.Now()

	templates, err := s.BookingRepository.FindScheduleTemplatesByBranch(ctx, branchID)
	if err != nil {
		return nil, err
	}

	var slots []*db.BunAppointmentSlot
	for _, tmpl := range templates {
		if tmpl.PractitionerID != nil && *tmpl.PractitionerID != practitionerID {
			continue
		}
		if !tmpl.IsActive || tmpl.Status != "ACTIVE" {
			continue
		}

		start := tmpl.StartTime
		end := tmpl.EndTime
		dur := tmpl.SlotDuration

		for start.Before(end) {
			slotEnd := start.Add(time.Duration(dur) * time.Minute)
			if slotEnd.After(end) {
				break
			}

			var pid *string
			if tmpl.PractitionerID != nil {
				pid = tmpl.PractitionerID
			}

			slot := &db.BunAppointmentSlot{
				ID:                 uuid.NewString(),
				TenantID:           tctx.TenantID,
				ScheduleTemplateID: tmpl.ID,
				BranchID:           branchID,
				PractitionerID:     pid,
				StartAt:            start,
				EndAt:              slotEnd,
				Capacity:           tmpl.Capacity,
				BookedCount:        0,
				Status:             "AVAILABLE",
				GeneratedAt:        now,
				IsActive:           true,
				CreatedAt:          now,
				UpdatedAt:          now,
				CreatedAction:      "GENERATE",
				UpdatedAction:      "GENERATE",
			}
			if err := s.BookingRepository.CreateAppointmentSlot(ctx, slot); err != nil {
				return nil, err
			}
			slots = append(slots, slot)

			start = slotEnd.Add(time.Duration(tmpl.BufferDuration) * time.Minute)
		}
	}
	for _, sl := range slots {
		s.cacheDel(ctx, cache.Key("booking", "appointment_slot", sl.ID))
	}
	return slots, nil
}

func (s *BookingService) UpdateSlotStatus(ctx context.Context, id, status string) (*db.BunAppointmentSlot, error) {
	slot, err := s.BookingRepository.FindAppointmentSlotByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if slot == nil {
		return nil, nil
	}
	slot.Status = status
	slot.UpdatedAt = time.Now()
	slot.UpdatedAction = "UPDATE_STATUS"
	if err := s.BookingRepository.UpdateAppointmentSlot(ctx, slot); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment_slot", id))
	return slot, nil
}

// Appointment

func (s *BookingService) GetAppointmentByID(ctx context.Context, id string) (*db.BunAppointment, error) {
	ck := cache.Key("booking", "appointment", id)
	var cached db.BunAppointment
	if ok, _ := s.cacheGet(ctx, ck, &cached); ok {
		return &cached, nil
	}
	m, err := s.BookingRepository.FindAppointmentByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.cacheSet(ctx, ck, m)
	return m, nil
}

func (s *BookingService) GetAppointments(ctx context.Context, filter model.AppointmentsFilter) ([]*db.BunAppointment, error) {
	return s.BookingRepository.FindAppointmentsByFilter(ctx, filter.BranchID, filter.PatientID, filter.PractitionerID, filter.Status, filter.ScheduledStart, filter.ScheduledEnd)
}

func (s *BookingService) CreateAppointment(ctx context.Context, input model.AppointmentInput) (*db.BunAppointment, error) {
	tctx := sharedCtx.FromContext(ctx)
	if tctx.TenantID == "" {
		return nil, fmt.Errorf("missing tenant context")
	}
	if tctx.BranchID == "" {
		return nil, fmt.Errorf("missing branch context")
	}
	now := time.Now()

	var patientID *string
	if input.PatientID != "" {
		patientID = &input.PatientID
	}
	var practitionerID *string
	if input.PractitionerID != "" {
		practitionerID = &input.PractitionerID
	}
	startAt := parseTime(input.ScheduledStart)
	endAt := parseTime(input.ScheduledEnd)

	if err := s.validateWithinBranchHours(ctx, tctx.BranchID, startAt, endAt); err != nil {
		return nil, err
	}

	notes := ""
	if input.Note != nil {
		notes = *input.Note
	}

	var appointmentTypeID *string
	if input.AppointmentTypeID != nil && *input.AppointmentTypeID != "" {
		t, err := s.BookingRepository.GetAppointmentTypeByID(ctx, *input.AppointmentTypeID)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, fmt.Errorf("appointment type not found: %s", *input.AppointmentTypeID)
		}
		appointmentTypeID = &t.ID
	}

	var disciplineID *string
	if input.DisciplineID != nil && *input.DisciplineID != "" {
		d, err := s.BookingRepository.GetTherapyDisciplineByID(ctx, *input.DisciplineID)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, fmt.Errorf("therapy discipline not found: %s", *input.DisciplineID)
		}
		if input.AppointmentTypeID != nil && *input.AppointmentTypeID != "" {
			offered, err := s.BookingRepository.ListDisciplinesByType(ctx, *input.AppointmentTypeID)
			if err != nil {
				return nil, err
			}
			linked := false
			for _, o := range offered {
				if o.ID == d.ID {
					linked = true
					break
				}
			}
			if !linked {
				return nil, fmt.Errorf("discipline is not offered for the selected appointment type")
			}
		}
		disciplineID = &d.ID
	}

	priceCents := 0
	if appointmentTypeID != nil && s.Payments != nil {
		// Snapshot the branch's price now; later catalog changes must not
		// rewrite what this booking owes.
		if price, offered, err := s.Payments.GetBranchPrice(ctx, tctx.BranchID, *appointmentTypeID); err == nil && offered {
			priceCents = price
		}
	}

	m := &db.BunAppointment{
		ID:                uuid.NewString(),
		TenantID:          tctx.TenantID,
		AppointmentSlotID: input.SlotID,
		BranchID:          tctx.BranchID,
		PatientID:         patientID,
		PractitionerID:    practitionerID,
		AppointmentTypeID: appointmentTypeID,
		DisciplineID:      disciplineID,
		PriceCents:        priceCents,
		StartAt:           startAt,
		EndAt:             endAt,
		Status:            "PENDING",
		Notes:             notes,
		CreatedAt:         now,
		UpdatedAt:         now,
		CreatedAction:     "CREATE",
		UpdatedAction:     "CREATE",
	}
	if err := s.BookingRepository.CreateAppointment(ctx, m); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", m.ID))
	return m, nil
}

func (s *BookingService) UpdateAppointment(ctx context.Context, id string, input model.AppointmentUpdateInput) (*db.BunAppointment, error) {
	existing, err := s.BookingRepository.FindAppointmentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if input.Status != nil {
		existing.Status = *input.Status
	}
	if input.Note != nil {
		existing.Notes = *input.Note
	}
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "UPDATE"
	if err := s.BookingRepository.UpdateAppointment(ctx, existing); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", id))
	return existing, nil
}

func (s *BookingService) ConfirmAppointment(ctx context.Context, id string) (*db.BunAppointment, error) {
	existing, err := s.BookingRepository.FindAppointmentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if existing.Status != "PENDING" {
		return nil, fmt.Errorf("cannot confirm appointment in status %s", existing.Status)
	}
	existing.Status = "CONFIRMED"
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "CONFIRM"
	if err := s.BookingRepository.UpdateAppointment(ctx, existing); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", id))
	return existing, nil
}

func (s *BookingService) StartAppointment(ctx context.Context, id string, goalIDs []string) (*db.BunAppointment, error) {
	existing, err := s.BookingRepository.FindAppointmentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if existing.Status != "CONFIRMED" {
		return nil, fmt.Errorf("cannot start appointment in status %s", existing.Status)
	}
	now := time.Now()
	start := existing.StartAt
	startToday := start.Year() == now.Year() && start.YearDay() == now.YearDay()
	if !startToday {
		return nil, fmt.Errorf("appointment cannot be started: scheduled for %s, not today", start.Format(time.RFC3339))
	}
	if now.Before(start) {
		return nil, fmt.Errorf("appointment cannot be started before scheduled time %s", start.Format(time.RFC3339))
	}
	if s.Payments != nil && existing.PriceCents > 0 && existing.PatientID != nil {
		if mode, err := s.Payments.GetBranchPaymentMode(ctx, existing.BranchID); err == nil && mode == "upfront" {
			if err := s.Payments.HoldFunds(ctx, existing.TenantID, existing.BranchID, *existing.PatientID, existing.ID, existing.PriceCents); err != nil {
				return nil, fmt.Errorf("cannot start session: %w", err)
			}
		}
	}
	existing.Status = "IN_PROGRESS"
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "START"
	if err := s.BookingRepository.UpdateAppointment(ctx, existing); err != nil {
		return nil, err
	}
	if len(goalIDs) > 0 {
		if _, err := s.SetAppointmentGoals(ctx, id, goalIDs); err != nil {
			return nil, err
		}
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", id))
	return existing, nil
}

func (s *BookingService) CompleteAppointment(ctx context.Context, id string) (*db.BunAppointment, error) {
	existing, err := s.BookingRepository.FindAppointmentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if existing.Status != "IN_PROGRESS" {
		return nil, fmt.Errorf("cannot complete appointment in status %s", existing.Status)
	}
	if s.Payments != nil && existing.PriceCents > 0 && existing.PatientID != nil {
		mode, err := s.Payments.GetBranchPaymentMode(ctx, existing.BranchID)
		if err != nil {
			return nil, err
		}
		if mode == "upfront" {
			err = s.Payments.CaptureHold(ctx, existing.TenantID, existing.BranchID, *existing.PatientID, existing.ID, existing.PriceCents)
		} else {
			err = s.Payments.ConsumeForAppointment(ctx, existing.TenantID, existing.BranchID, *existing.PatientID, existing.ID, existing.PriceCents)
		}
		if err != nil {
			return nil, fmt.Errorf("cannot complete session: %w", err)
		}
	}
	existing.Status = "DONE"
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "COMPLETE"
	if err := s.BookingRepository.UpdateAppointment(ctx, existing); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", id))
	return existing, nil
}

func (s *BookingService) CancelAppointment(ctx context.Context, id string, reason string) (*db.BunAppointment, error) {
	existing, err := s.BookingRepository.FindAppointmentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if existing.Status == "DONE" || existing.Status == "CANCELLED" {
		return nil, fmt.Errorf("cannot cancel appointment in status %s", existing.Status)
	}
	now := time.Now()
	existing.Status = "CANCELLED"
	existing.CancellationReason = reason
	existing.CancelledAt = &now
	existing.UpdatedAt = now
	existing.UpdatedAction = "CANCEL"
	if err := s.BookingRepository.UpdateAppointment(ctx, existing); err != nil {
		return nil, err
	}
	if s.Payments != nil {
		_ = s.Payments.ReleaseHold(ctx, id)
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", id))
	return existing, nil
}

func (s *BookingService) RescheduleAppointment(ctx context.Context, id string, input model.RescheduleAppointmentInput) (*db.BunAppointment, error) {
	existing, err := s.BookingRepository.FindAppointmentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if existing.Status == "DONE" || existing.Status == "CANCELLED" {
		return nil, fmt.Errorf("cannot reschedule appointment in status %s", existing.Status)
	}
	startAt, err := time.Parse(time.RFC3339, input.ScheduledStart)
	if err != nil {
		return nil, fmt.Errorf("invalid scheduled_start: %w", err)
	}
	endAt, err := time.Parse(time.RFC3339, input.ScheduledEnd)
	if err != nil {
		return nil, fmt.Errorf("invalid scheduled_end: %w", err)
	}

	if err := s.validateWithinBranchHours(ctx, existing.BranchID, startAt, endAt); err != nil {
		return nil, err
	}

	now := time.Now()
	oldStatus := existing.Status
	newPractitionerID := existing.PractitionerID
	if input.PractitionerID != nil && *input.PractitionerID != "" {
		newPractitionerID = input.PractitionerID
	}

	existing.Status = "RESCHEDULED"
	existing.RescheduleReason = input.Reason
	existing.RescheduledToID = nil
	existing.UpdatedAt = now
	existing.UpdatedAction = "RESCHEDULE"
	if err := s.BookingRepository.UpdateAppointment(ctx, existing); err != nil {
		return nil, err
	}

	newAppt := &db.BunAppointment{
		ID:                uuid.New().String(),
		TenantID:          existing.TenantID,
		BranchID:          existing.BranchID,
		PatientID:         existing.PatientID,
		PractitionerID:    newPractitionerID,
		AppointmentSlotID: existing.AppointmentSlotID,
		StartAt:           startAt,
		EndAt:             endAt,
		Status:            oldStatus,
		Notes:             existing.Notes,
		RescheduledFromID: &existing.ID,
		CreatedAt:         now,
		UpdatedAt:         now,
		CreatedAction:     "RESCHEDULE",
		UpdatedAction:     "RESCHEDULE",
	}
	if err := s.BookingRepository.CreateAppointment(ctx, newAppt); err != nil {
		return nil, err
	}

	existing.RescheduledToID = &newAppt.ID
	if err := s.BookingRepository.UpdateAppointment(ctx, existing); err != nil {
		return nil, err
	}

	s.cacheDel(ctx, cache.Key("booking", "appointment", id))
	s.cacheDel(ctx, cache.Key("booking", "appointment", newAppt.ID))
	return newAppt, nil
}

// ScheduleException

func (s *BookingService) GetScheduleExceptionByID(ctx context.Context, id string) (*db.BunScheduleException, error) {
	ck := cache.Key("booking", "schedule_exception", id)
	var cached db.BunScheduleException
	if ok, _ := s.cacheGet(ctx, ck, &cached); ok {
		return &cached, nil
	}
	m, err := s.BookingRepository.FindScheduleExceptionByID(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.cacheSet(ctx, ck, m)
	return m, nil
}

func (s *BookingService) GetScheduleExceptions(ctx context.Context, branchID, practitionerID *string) ([]*db.BunScheduleException, error) {
	if branchID != nil && *branchID != "" {
		return s.BookingRepository.FindScheduleExceptionsByBranch(ctx, *branchID)
	}
	if practitionerID != nil && *practitionerID != "" {
		return s.BookingRepository.FindScheduleExceptionsByPractitioner(ctx, *practitionerID)
	}
	return s.BookingRepository.ListScheduleExceptions(ctx)
}

func (s *BookingService) CreateScheduleException(ctx context.Context, input model.ScheduleExceptionInput) (*db.BunScheduleException, error) {
	tctx := sharedCtx.FromContext(ctx)
	now := time.Now()

	excDate := parseTime(input.ExceptionDate)
	startAt := time.Date(excDate.Year(), excDate.Month(), excDate.Day(), 0, 0, 0, 0, time.UTC)
	endAt := time.Date(excDate.Year(), excDate.Month(), excDate.Day(), 23, 59, 59, 0, time.UTC)

	m := &db.BunScheduleException{
		ID:             uuid.NewString(),
		TenantID:       tctx.TenantID,
		PractitionerID: &input.PractitionerID,
		Type:           "OVERRIDE",
		Reason:         "Schedule exception",
		StartAt:        startAt,
		EndAt:          endAt,
		Status:         "ACTIVE",
		IsActive:       true,
		CreatedAt:      now,
		UpdatedAt:      now,
		CreatedAction:  "CREATE",
		UpdatedAction:  "CREATE",
	}
	if tctx.BranchID != "" {
		branchID := tctx.BranchID
		m.ScheduleTemplateID = &branchID
	}
	if err := s.BookingRepository.CreateScheduleException(ctx, m); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "schedule_exception", m.ID))
	return m, nil
}

func (s *BookingService) DeleteScheduleException(ctx context.Context, id string) (bool, error) {
	existing, err := s.BookingRepository.FindScheduleExceptionByID(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	existing.IsActive = false
	existing.Status = "CANCELLED"
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "DELETE"
	if err := s.BookingRepository.UpdateScheduleException(ctx, existing); err != nil {
		return false, err
	}
	s.cacheDel(ctx, cache.Key("booking", "schedule_exception", id))
	return true, nil
}

// Appointment Goals

func (s *BookingService) GetAppointmentGoals(ctx context.Context, appointmentID string) ([]*db.BunAppointmentGoal, error) {
	return s.BookingRepository.FindAppointmentGoals(ctx, appointmentID)
}

func (s *BookingService) SetAppointmentGoals(ctx context.Context, appointmentID string, goalIDs []string) ([]*db.BunAppointmentGoal, error) {
	if err := s.BookingRepository.RemoveAppointmentGoals(ctx, appointmentID); err != nil {
		return nil, err
	}
	tctx := sharedCtx.FromContext(ctx)
	now := time.Now()
	list := make([]*db.BunAppointmentGoal, 0, len(goalIDs))
	for _, gid := range goalIDs {
		list = append(list, &db.BunAppointmentGoal{
			ID:            uuid.NewString(),
			TenantID:      tctx.TenantID,
			AppointmentID: appointmentID,
			GoalID:        gid,
			Progress:      0,
			Status:        "Not Started",
			IsActive:      true,
			CreatedAt:     now,
			UpdatedAt:     now,
			CreatedAction: "CREATE",
		})
	}
	if len(list) > 0 {
		if err := s.BookingRepository.CreateAppointmentGoals(ctx, list); err != nil {
			return nil, err
		}
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", appointmentID))
	return s.BookingRepository.FindAppointmentGoals(ctx, appointmentID)
}

func (s *BookingService) UpdateAppointmentGoal(ctx context.Context, id string, input model.AppointmentGoalUpdateInput) (*db.BunAppointmentGoal, error) {
	existing, err := s.BookingRepository.FindAppointmentGoalByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if input.Progress != nil {
		existing.Progress = *input.Progress
	}
	if input.Status != nil && *input.Status != "" {
		existing.Status = *input.Status
	}
	if input.Notes != nil {
		existing.Notes = *input.Notes
	}
	existing.UpdatedAt = time.Now()
	existing.UpdatedAction = "UPDATE"
	if err := s.BookingRepository.UpdateAppointmentGoal(ctx, existing); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", existing.AppointmentID))
	return existing, nil
}

func (s *BookingService) RemoveAppointmentGoal(ctx context.Context, id string) (bool, error) {
	existing, err := s.BookingRepository.FindAppointmentGoalByID(ctx, id)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	if err := s.BookingRepository.RemoveAppointmentGoal(ctx, id); err != nil {
		return false, err
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", existing.AppointmentID))
	return true, nil
}

// SOAP Notes

func (s *BookingService) GetAppointmentSoap(ctx context.Context, appointmentID string) (*db.BunSoapNote, error) {
	return s.BookingRepository.FindSoapNoteByAppointment(ctx, appointmentID)
}

func (s *BookingService) UpsertSoapNote(ctx context.Context, appointmentID string, input model.SoapNoteInput) (*db.BunSoapNote, error) {
	existing, err := s.BookingRepository.FindSoapNoteByAppointment(ctx, appointmentID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if existing == nil {
		tctx := sharedCtx.FromContext(ctx)
		existing = &db.BunSoapNote{
			ID:            uuid.NewString(),
			TenantID:      tctx.TenantID,
			AppointmentID: appointmentID,
			Subjective:    derefStr(input.Subjective),
			Objective:     derefStr(input.Objective),
			Assessment:    derefStr(input.Assessment),
			Plan:          derefStr(input.Plan),
			CreatedAt:     now,
			UpdatedAt:     now,
			CreatedAction: "CREATE",
		}
		if err := s.BookingRepository.CreateSoapNote(ctx, existing); err != nil {
			return nil, err
		}
	} else {
		if input.Subjective != nil {
			existing.Subjective = *input.Subjective
		}
		if input.Objective != nil {
			existing.Objective = *input.Objective
		}
		if input.Assessment != nil {
			existing.Assessment = *input.Assessment
		}
		if input.Plan != nil {
			existing.Plan = *input.Plan
		}
		existing.UpdatedAt = now
		existing.UpdatedAction = "UPDATE"
		if err := s.BookingRepository.UpdateSoapNote(ctx, existing); err != nil {
			return nil, err
		}
	}
	s.cacheDel(ctx, cache.Key("booking", "appointment", appointmentID))
	return existing, nil
}

func (s *BookingService) GetAppointmentGoalByID(ctx context.Context, id string) (*db.BunAppointmentGoal, error) {
	return s.BookingRepository.FindAppointmentGoalByID(ctx, id)
}

func (s *BookingService) GetSoapNoteByID(ctx context.Context, id string) (*db.BunSoapNote, error) {
	return s.BookingRepository.FindSoapNoteByID(ctx, id)
}

// Helpers

func parseTimeVal(s string) time.Time {
	t, err := time.Parse("15:04", s)
	if err != nil {
		t, err = time.Parse("15:04:05", s)
		if err != nil {
			return time.Time{}
		}
	}
	return time.Date(2000, 1, 1, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("15:04")
}

func formatDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func rruleFromDay(d int) string {
	code, ok := dayToRRULE[d]
	if !ok {
		return ""
	}
	return fmt.Sprintf("FREQ=WEEKLY;BYDAY=%s", code)
}

// daysFromRRULE expands a BYDAY list into weekday indexes (Monday = 0 …
// Sunday = 6). A single row can cover several days, e.g.
// "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR".
func daysFromRRULE(rrule string) []int {
	const marker = "BYDAY="
	idx := strings.Index(rrule, marker)
	if idx < 0 {
		return nil
	}
	var out []int
	for _, code := range strings.Split(rrule[idx+len(marker):], ",") {
		if d, ok := rruleToDay[strings.TrimSpace(code)]; ok {
			out = append(out, d)
		}
	}
	return out
}

// ruleCoversDay reports whether a branch-hours row applies to a weekday.
// Matching must read the whole BYDAY list — scanning the rule for a code
// would also match "WE" inside "FREQ=WEEKLY".
func ruleCoversDay(rrule string, day int) bool {
	for _, d := range daysFromRRULE(rrule) {
		if d == day {
			return true
		}
	}
	return false
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now()
	}
	return t
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
