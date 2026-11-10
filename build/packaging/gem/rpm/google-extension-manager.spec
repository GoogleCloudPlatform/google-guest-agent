# Copyright 2026 Google LLC

# <rpm_header> - pre processed rpm header, intended for automation use.

%define debug_package %{nil}

%if ! 0%{?prebuilt}
Name: google-extension-manager
Version: %{_version}
Release: g1%{?dist}
Source0: %{name}_%{version}.orig.tar.gz
BuildArch: %{_arch}
%endif

Epoch:   1
Summary: Google Extension Manager (standalone plugin manager).
License: ASL 2.0
Url: https://cloud.google.com/compute/docs/vm-extensions
Vendor: Google LLC

%description
Provides the standalone Google Extension Manager daemon for managing VM
extensions without GCE CorePlugin or GCE Metadata Server dependencies. Until
off-GCP ACS authentication (MetadataInitFunc) is configured, ACS communication
remains inactive unless an explicit non-MDS ACS transport is configured.

%if ! 0%{?prebuilt}
%prep
%autosetup

%build
make cmd/google_extension_manager/google_extension_manager VERSION=%{_version}

%install
install -d %{buildroot}%{_bindir}
install -d %{buildroot}/usr/share/google-extension-manager
install -d %{buildroot}/usr/lib/systemd/system
install -p -m 0644 build/packaging/gem/configs/usr/share/google-extension-manager/gem.cfg %{buildroot}/usr/share/google-extension-manager/gem.cfg
install -p -m 0644 build/packaging/gem/configs/usr/lib/systemd/system/google-extension-manager.service %{buildroot}/usr/lib/systemd/system/google-extension-manager.service
install -p -m 0755 cmd/google_extension_manager/google_extension_manager %{buildroot}%{_bindir}/google_extension_manager

%files
/usr/share/google-extension-manager/gem.cfg
/usr/lib/systemd/system/google-extension-manager.service
%{_bindir}/google_extension_manager
%endif

%post
if [ $1 -eq 1 ]; then
  mkdir -p /etc/google-extension-manager
  if [ ! -f /etc/google-extension-manager/gem.cfg ]; then
    cp -a /usr/share/google-extension-manager/gem.cfg /etc/google-extension-manager/gem.cfg
  fi
  systemctl enable google-extension-manager.service >/dev/null 2>&1 || :
  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || :
    if [ -s /etc/google-extension-manager/machine_id ] || grep -Eq '^[[:space:]]*instance_id[[:space:]]*=[[:space:]]*[^[:space:]#;]' /etc/google-extension-manager/gem.cfg 2>/dev/null; then
      systemctl start google-extension-manager.service >/dev/null 2>&1 || :
    fi
  fi
else
  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || :
    systemctl try-restart google-extension-manager.service >/dev/null 2>&1 || :
  fi
fi

%preun
if [ $1 -eq 0 ]; then
  systemctl --no-reload disable google-extension-manager.service >/dev/null 2>&1 || :
  if [ -d /run/systemd/system ]; then
    systemctl stop google-extension-manager.service >/dev/null 2>&1 || :
  fi
  if [ -x /usr/bin/google_extension_manager ]; then
    /usr/bin/google_extension_manager -cleanup || :
  fi
fi

%postun
if [ $1 -eq 0 ]; then
  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || :
  fi
fi

# <rpm_footer> - pre processed rpm footer, intended for automation use.
