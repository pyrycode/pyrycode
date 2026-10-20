package claudetape

// Linux kernel observations are unavailable here; strict inventory accounting
// still rejects every unjoined Wait.
func (a *FallbackLaunchAudit) sample() {}

func (a *FallbackLaunchAudit) bindRoot(int) {}
