Name: huatuo-bamai
Version: 2.1.0
Release: 3%{?dist}
Summary: Huatuo is a cloud-native operating system observability project

# Disable debug package and build-id generation
%global debug_package %{nil}
%global _build_id_links none

Group: System Environment/Daemons
URL: https://huatuo.tech/
License: APLv2

Source0: https://github.com/ccfos/huatuo/archive/tags/tags/v%{version}.tar.gz
Source1: huatuo-bamai.service
Source2: grafana-example.zip

# Support multiple architectures
ExclusiveArch: x86_64 aarch64

# Build dependencies
BuildRequires: make
BuildRequires: clang
BuildRequires: libbpf-devel
BuildRequires: bpftool
BuildRequires: curl
BuildRequires: git
BuildRequires: golang
BuildRequires: unzip

# Runtime dependencies
Requires: systemd
Requires: glibc >= 2.17
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

%description
Huatuo is a cloud-native operating system observability project that provides
comprehensive monitoring and analysis capabilities for system performance,
network behavior, and resource utilization using eBPF technology.

%prep
%setup -q -n huatuo-%{version}

%build
# Build from source
make

%check
# Basic checks for compiled binaries
echo "Running package checks..."

# Check if main binary exists and is executable
if [ ! -f "_output/bin/huatuo-bamai" ]; then
    echo "ERROR: Main binary _output/bin/huatuo-bamai not found"
    exit 1
fi

if [ ! -x "_output/bin/huatuo-bamai" ]; then
    echo "ERROR: Main binary _output/bin/huatuo-bamai is not executable"
    exit 1
fi

# Check binary architecture matches build target
file _output/bin/huatuo-bamai | grep -q "%{_target_cpu}" || {
    echo "WARNING: Binary architecture may not match target architecture %{_target_cpu}"
}

# Check if required directories exist
for dir in _output/conf _output/bpf; do
    if [ ! -d "$dir" ]; then
        echo "ERROR: Required directory $dir not found"
        exit 1
    fi
done

# Check if LICENSE file exists
if [ ! -f "LICENSE" ]; then
    echo "ERROR: LICENSE file not found"
    exit 1
fi

# Basic binary validation - check if it can show help/version
timeout 10s ./_output/bin/huatuo-bamai --help >/dev/null 2>&1 || {
    echo "WARNING: Binary may not be properly linked or missing dependencies"
}

echo "Package checks completed successfully"

%install
rm -rf %{buildroot}

# Modify configuration file
sed -i 's/"http:\/\/127.0.0.1:9200"/""/' _output/conf/huatuo-bamai.conf

# Install main application to /opt
mkdir -p %{buildroot}/opt/huatuo-bamai
cp -r _output/bin _output/conf _output/bpf LICENSE %{buildroot}/opt/huatuo-bamai/

# Install grafana-example directory (extract from zip)
mkdir -p %{buildroot}/opt/huatuo-bamai/grafana-example
unzip -q %{SOURCE2} -d %{buildroot}/opt/huatuo-bamai/grafana-example/

# Create symlink for main executable
mkdir -p %{buildroot}/usr/local/bin
ln -s ../../opt/huatuo-bamai/bin/huatuo-bamai %{buildroot}/usr/local/bin/huatuo-bamai

# Install systemd service files
mkdir -p %{buildroot}/etc/systemd/system

# Install default service file
install -m 644 %{SOURCE1} %{buildroot}/etc/systemd/system/huatuo-bamai.service

# Create template service file for multiple regions
sed 's/--region example/--region %i/g' %{SOURCE1} > %{buildroot}/etc/systemd/system/huatuo-bamai@.service

# Create configuration directory
mkdir -p %{buildroot}/etc/huatuo-bamai
ln -s ../../opt/huatuo-bamai/conf/huatuo-bamai.conf %{buildroot}/etc/huatuo-bamai/huatuo-bamai.conf

# Create log and data directories
mkdir -p %{buildroot}/var/log/huatuo-bamai
mkdir -p %{buildroot}/var/lib/huatuo-bamai

# Set proper permissions
chmod 755 %{buildroot}/opt/huatuo-bamai/bin/huatuo-bamai

%clean
rm -rf %{buildroot}

%files
%defattr(-,root,root,-)
/opt/huatuo-bamai/bin/
/opt/huatuo-bamai/conf/
/opt/huatuo-bamai/bpf/
/opt/huatuo-bamai/grafana-example/
/usr/local/bin/huatuo-bamai
/etc/systemd/system/huatuo-bamai.service
/etc/systemd/system/huatuo-bamai@.service
/etc/huatuo-bamai/huatuo-bamai.conf
%dir /var/log/huatuo-bamai
%dir /var/lib/huatuo-bamai
%doc /opt/huatuo-bamai/LICENSE

%post
%systemd_post huatuo-bamai.service
# Enable default service but don't start it automatically
systemctl enable huatuo-bamai.service >/dev/null 2>&1 || :
echo "Service installed. Usage examples:"
echo "  Single instance: systemctl start huatuo-bamai"
echo "  Multiple regions: systemctl start huatuo-bamai@region1"
echo "  Configure region in: /etc/huatuo-bamai/huatuo-bamai.conf"
echo "Architecture: %{_target_cpu}"

%preun
%systemd_preun huatuo-bamai.service
if [ $1 -eq 0 ]; then
    # Stop any running template instances
    systemctl stop 'huatuo-bamai@*.service' >/dev/null 2>&1 || :
fi

%postun
if [ $1 -eq 0 ]; then
    # Complete removal - stop and disable services
    systemctl stop huatuo-bamai.service >/dev/null 2>&1 || :
    systemctl stop 'huatuo-bamai@*.service' >/dev/null 2>&1 || :
    systemctl disable huatuo-bamai.service >/dev/null 2>&1 || :
    systemctl daemon-reload >/dev/null 2>&1 || :
else
    # Upgrade - restart service if it was running
    systemctl try-restart huatuo-bamai.service >/dev/null 2>&1 || :
fi

%changelog
* Mon Dec 23 2025 panzerzheng <panzerzheng@tencent.com> - 2.1.0-3
- [Type] optimization
- [DESC] Optimized Grafana configuration files packaging
- Consolidated individual yaml and json files into grafana-example.zip
- Simplified spec file installation process

* Wed Nov 05 2025 panzerzheng <panzerzheng@tencent.com> - 2.1.0-2
- [Type] bugfix
- [DESC] Fixed postun script error during package removal

* Tue Nov 04 2025 panzerzheng <panzerzheng@tencent.com> - 2.1.0-1
- [Type] other
- [DESC] Upgraded package and modified Grafana configuration templates
- Updated package structure and improved Grafana dashboard configurations
- Enhanced template service configuration for multi-region deployment

* Mon Oct 28 2024 panzerzheng <panzerzheng@tencent.com> - 2.0.0-1
- [Type] other
- [DESC] Initial RPM package for huatuo-bamai 2.0.0
