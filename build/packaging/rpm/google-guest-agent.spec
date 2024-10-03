# Copyright 2023 Google Inc.

# <rpm_header> - pre processed rpm header, intended for automation use.

%define debug_package %{nil}

%if ! 0%{?prebuilt}
Name: google-guest-agent
Version: %{_version}
Release: g1%{?dist}
Source0: %{name}_%{version}.orig.tar.gz
BuildArch: %{_arch}
%endif

Epoch:   2
Summary: Google Compute Engine guest agent.
License: ASL 2.0
Url: https://cloud.google.com/compute/docs/images/guest-environment
Vendor: Google, Inc.
Requires: google-compute-engine-oslogin >= 1:20231003
Obsoletes: python-google-compute-engine, python3-google-compute-engine

%description
Contains the Google guest agent binary.

%if ! 0%{?prebuilt}
%prep
%autosetup

%build
./build/build.sh --version=%{_version}

%install
install -d %{buildroot}%{_bindir}
install -p -m 0644 build/configs/etc/default/instance_configs.cfg %{buildroot}%/usr/share/google-guest-agent/instance_configs.cfg
install -p -m 0644 build/configs/usr/lib/systemd/system/google-guest-agent.service %{buildroot}%/usr/lib/systemd/system/google-guest-agent.service
install -p -m 0644 build/configs/usr/lib/systemd/system/google-guest-agent.service %{buildroot}%/usr/lib/systemd/system/google-startup-scripts.service
install -p -m 0644 build/configs/usr/lib/systemd/system/google-guest-agent.service %{buildroot}%/usr/lib/systemd/system/google-shutdown-scripts.service
install -p -m 0755 cmd/google_guest_agent/google_guest_agent %{buildroot}%{_bindir}/google_guest_agent
install -p -m 0755 cmd/google_guest_agent/ggactl %{buildroot}%{_bindir}/ggactl
install -p -m 0755 cmd/core_plugin/core_plugin %{buildroot}%{_libdir}/google/guest_agent/core_plugin

%files
/etc/default/instance_configs.cfg
/usr/lib/systemd/system/google-guest-agent.service
/usr/lib/systemd/system/google-startup-scripts.service
/usr/lib/systemd/system/google-shutdown-scripts.service
%{_bindir}/google_guest_agent
%{_bindir}/ggactl
%{_libdir}/google/guest_agent/core_plugin

%endif

%post
  # Initial installation
if [ $1 -eq 1 ]; then
  # Install instance configs if not already present.
  if [ ! -f /etc/default/instance_configs.cfg ]; then
    cp -a /usr/share/google-guest-agent/instance_configs.cfg /etc/default/
  fi

  # Use enable instead of preset because preset is not supported in
  # chroots.
  systemctl enable google-guest-agent.service >/dev/null 2>&1 || :
  systemctl enable google-startup-scripts.service >/dev/null 2>&1 || :
  systemctl enable google-shutdown-scripts.service >/dev/null 2>&1 || :

  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || :
    systemctl start google-guest-agent.service >/dev/null 2>&1 || :
  fi

else
  # Package upgrade
  if [ -d /run/systemd/system ]; then
    systemctl try-restart google-guest-agent.service >/dev/null 2>&1 || :
  fi
fi

%preun
if [ $1 -eq 0 ]; then
  # Package removal, not upgrade
  systemctl --no-reload disable google-guest-agent.service >/dev/null 2>&1 || :
  systemctl --no-reload disable google-startup-scripts.service >/dev/null 2>&1 || :
  systemctl --no-reload disable google-shutdown-scripts.service >/dev/null 2>&1 || :
  if [ -d /run/systemd/system ]; then
    systemctl stop google-guest-agent.service >/dev/null 2>&1 || :
  fi
fi

%postun
if [ $1 -eq 0 ]; then
  # Package removal, not upgrade

  if [ -f /etc/default/instance_configs.cfg ]; then
    rm /etc/default/instance_configs.cfg
  fi

  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || :
  fi
fi

# <rpm_footer> - pre processed rpm footer, intended for automation use.