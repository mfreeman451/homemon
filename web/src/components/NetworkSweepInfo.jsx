import React from 'react';
import { Info } from 'lucide-react';

const NetworkSweepInfo = ({ sweepDetails }) => {
    // Parse sweep configuration from the details
    const networks = sweepDetails?.network?.split(',') || [];
    const ports = sweepDetails?.ports || [];

    return (
        <div className="bg-white rounded-lg shadow p-4">
            <div className="flex items-center gap-2 mb-4">
                <Info className="w-5 h-5 text-blue-500" />
                <h3 className="text-lg font-semibold">Sweep Configuration</h3>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                {/* Networks section */}
                <div className="space-y-2">
                    <h4 className="font-medium text-gray-700">Networks Monitored</h4>
                    <div className="space-y-1">
                        {networks.map((network, idx) => (
                            <div key={idx} className="text-sm bg-gray-50 p-2 rounded">
                                {network.trim()}
                            </div>
                        ))}
                    </div>
                </div>

                {/* Ports section */}
                <div className="space-y-2">
                    <h4 className="font-medium text-gray-700">Ports Scanned</h4>
                    <div className="flex flex-wrap gap-2">
                        {ports.map((port, idx) => (
                            <div key={idx} className="text-sm bg-gray-50 px-3 py-1 rounded">
                                {port}
                            </div>
                        ))}
                    </div>
                </div>
            </div>

            {/* Statistics */}
            <div className="mt-4 grid grid-cols-2 md:grid-cols-4 gap-4">
                <div className="bg-gray-50 p-3 rounded">
                    <div className="text-sm text-gray-600">Total Hosts</div>
                    <div className="text-lg font-semibold">{sweepDetails.total_hosts || 0}</div>
                </div>
                <div className="bg-gray-50 p-3 rounded">
                    <div className="text-sm text-gray-600">Responding</div>
                    <div className="text-lg font-semibold">{sweepDetails.available_hosts || 0}</div>
                </div>
                <div className="bg-gray-50 p-3 rounded">
                    <div className="text-sm text-gray-600">Last Sweep</div>
                    <div className="text-lg font-semibold">
                        {new Date(sweepDetails.last_sweep * 1000).toLocaleTimeString()}
                    </div>
                </div>
                <div className="bg-gray-50 p-3 rounded">
                    <div className="text-sm text-gray-600">Response Rate</div>
                    <div className="text-lg font-semibold">
                        {((sweepDetails.available_hosts / sweepDetails.total_hosts) * 100).toFixed(1)}%
                    </div>
                </div>
            </div>
        </div>
    );
};

export default NetworkSweepInfo;