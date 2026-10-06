# Use a single-application Wayland kiosk for the local console

The Experimental NAS is an ITX computer with its own display and input devices, not a headless server. A-NAS will present the same embedded Web desktop locally through an unprivileged Cage/Chromium Wayland kiosk instead of installing a general-purpose desktop environment; this gives the device an appliance-like console while keeping Product Service and Host Agent permissions unchanged and retaining the SSH tunnel as an optional remote development path.
