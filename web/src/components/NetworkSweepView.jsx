const NetworkSweepView = ({ nodeId, service, standalone = false }) => {
    const [viewMode, setViewMode] = useState('summary');
    const [searchTerm, setSearchTerm] = useState('');
    console.log('NetworkSweepView received service:', service);

    // Parse sweep details from service data
    let sweepDetails;
    try {
        // Try to parse from message first
        if (service.message) {
            sweepDetails = typeof service.message === 'string'
                ? JSON.parse(service.message)
                : service.message;
        }

        // Fall back to details if message parsing failed
        if (!sweepDetails && service.details) {
            sweepDetails = typeof service.details === 'string'
                ? JSON.parse(service.details)
                : service.details;
        }

        console.log('Parsed sweep details:', sweepDetails);

        if (!sweepDetails) {
            return (
                <div className="bg-white rounded-lg shadow p-6">
                    <div className="text-red-500">No sweep data available</div>
                    <pre className="mt-4 p-2 bg-gray-100 rounded overflow-auto">
                        {JSON.stringify(service, null, 2)}
                    </pre>
                </div>
            );
        }
    } catch (err) {
        console.error('Error parsing sweep details:', err);
        return (
            <div className="bg-white rounded-lg shadow p-6">
                <div className="text-red-500">Error parsing sweep data: {err.message}</div>
                <pre className="mt-4 p-2 bg-gray-100 rounded overflow-auto">
                    {JSON.stringify(service, null, 2)}
                </pre>
            </div>
        );
    }

    // Filter and format hosts data
    const filteredHosts = sweepDetails.hosts
        ? sweepDetails.hosts
            .filter(host => host.host.toLowerCase().includes(searchTerm.toLowerCase()))
            .sort((a, b) => {
                const aMatch = a.host.match(/(\d+)$/);
                const bMatch = b.host.match(/(\d+)$/);
                if (aMatch && bMatch) {
                    return parseInt(aMatch[1]) - parseInt(bMatch[1]);
                }
                return a.host.localeCompare(b.host);
            })
        : [];

    return (
        <div className={`space-y-6 ${!standalone && 'bg-white rounded-lg shadow p-4'}`}>
            {/* Network Sweep Info Component */}
            <NetworkSweepInfo sweepDetails={sweepDetails} />

            {/* Controls */}
            <div className="flex justify-between items-center gap-4">
                <div className="space-x-2">
                    <button
                        onClick={() => setViewMode('summary')}
                        className={`px-3 py-1 rounded ${
                            viewMode === 'summary' ? 'bg-blue-500 text-white' : 'bg-gray-100'
                        }`}
                    >
                        Summary
                    </button>
                    <button
                        onClick={() => setViewMode('hosts')}
                        className={`px-3 py-1 rounded ${
                            viewMode === 'hosts' ? 'bg-blue-500 text-white' : 'bg-gray-100'
                        }`}
                    >
                        Host Details
                    </button>
                </div>
                <ExportButton sweepDetails={sweepDetails} />
            </div>

            {viewMode === 'hosts' && (
                <div className="flex items-center gap-4">
                    <input
                        type="text"
                        placeholder="Search hosts..."
                        className="flex-1 p-2 border rounded"
                        value={searchTerm}
                        onChange={(e) => setSearchTerm(e.target.value)}
                    />
                </div>
            )}

            {/* Main Content */}
            {viewMode === 'summary' ? (
                <SweepSummaryView sweepDetails={sweepDetails} />
            ) : (
                <HostDetailsView hosts={filteredHosts} />
            )}
        </div>
    );
};

// Helper component for summary view
const SweepSummaryView = ({ sweepDetails }) => {
    if (!sweepDetails.ports) return null;

    const sortedPorts = [...sweepDetails.ports].sort((a, b) => b.available - a.available);

    return (
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-4">
            {sortedPorts.map(port => (
                <div key={port.port} className="bg-gray-50 p-3 rounded">
                    <div className="font-medium">Port {port.port}</div>
                    <div className="text-sm text-gray-600">
                        {port.available} hosts responding
                    </div>
                    <div className="mt-1 bg-gray-200 rounded-full h-2">
                        <div
                            className="bg-blue-500 rounded-full h-2"
                            style={{
                                width: `${(port.available / sweepDetails.total_hosts) * 100}%`
                            }}
                        />
                    </div>
                </div>
            ))}
        </div>
    );
};

// Helper component for host details view
const HostDetailsView = ({ hosts }) => (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        {hosts.map(host => (
            <div key={host.host} className="bg-white p-4 rounded-lg shadow">
                <div className="flex justify-between items-center">
                    <h4 className="text-lg font-semibold">{host.host}</h4>
                    <span className={`px-2 py-1 rounded ${
                        host.available ? 'bg-green-100 text-green-800' : 'bg-red-100 text-red-800'
                    }`}>
                        {host.available ? 'Online' : 'Offline'}
                    </span>
                </div>

                {/* Open Ports */}
                {host.port_results?.length > 0 && (
                    <div className="mt-4">
                        <h5 className="font-medium">Open Ports</h5>
                        <div className="grid grid-cols-2 gap-2 mt-2">
                            {host.port_results
                                .filter(port => port.available)
                                .map(port => (
                                    <div key={port.port} className="text-sm bg-gray-50 p-2 rounded">
                                        {port.port} {port.service && `(${port.service})`}
                                    </div>
                                ))}
                        </div>
                    </div>
                )}

                {/* ICMP Status */}
                {host.icmp_status && (
                    <div className="mt-4 bg-gray-50 p-3 rounded">
                        <h5 className="font-medium mb-2">ICMP Status</h5>
                        <div className="grid grid-cols-2 gap-2 text-sm">
                            <div>Response Time: {formatResponseTime(host.icmp_status.round_trip)}</div>
                            <div>Packet Loss: {formatPacketLoss(host.icmp_status.packet_loss)}</div>
                        </div>
                    </div>
                )}

                {/* Timestamps */}
                <div className="mt-4 text-xs text-gray-500">
                    <div>First seen: {new Date(host.first_seen).toLocaleString()}</div>
                    <div>Last seen: {new Date(host.last_seen).toLocaleString()}</div>
                </div>
            </div>
        ))}
    </div>
);

// Helper functions
const formatResponseTime = (time) => {
    if (!time && time !== 0) return 'N/A';
    return `${(time / 1000000).toFixed(2)}ms`;
};

const formatPacketLoss = (loss) => {
    if (typeof loss !== 'number') return 'N/A';
    return `${loss.toFixed(1)}%`;
};

export default NetworkSweepView;