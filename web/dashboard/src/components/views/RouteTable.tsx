import type { RouteStatus, StatsData } from "@/types/api";
import { StatusBadge } from "@/components/ui/StatusBadge";
import { Button } from "@/components/ui/Button";
import { Edit2, Trash2, Octagon } from "lucide-react";

interface RouteTableProps {
    routes: RouteStatus[];
    stats: StatsData | null;
    onEdit: (route: RouteStatus) => void;
    onDelete: (id: string) => void;
    onStop: (route: RouteStatus) => void;
    onSelect: (id: string) => void;
}

export function RouteTable({ routes, stats, onEdit, onDelete, onStop, onSelect }: RouteTableProps) {
    if (routes.length === 0) {
        return <div className="p-8 text-center text-gray-500 italic">No routes configured.</div>;
    }

    return (
        <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse">
                <thead className="bg-gray-700/50 text-gray-300 text-xs uppercase tracking-wider">
                    <tr>
                        <th className="px-6 py-3">Host</th>
                        <th className="px-6 py-3">Path</th>
                        <th className="px-6 py-3">Status / Deployment</th>
                        <th className="px-6 py-3">Target</th>
                        <th className="px-6 py-3">Requests</th>
                        <th className="px-6 py-3">Dependencies</th>
                        <th className="px-6 py-3 text-right">Actions</th>
                    </tr>
                </thead>
                <tbody className="divide-y divide-gray-700">
                    {routes.map((route) => (
                        <tr
                            key={route.id}
                            className="hover:bg-gray-700/30 transition-colors cursor-pointer"
                            onClick={() => onSelect(route.id)}
                        >
                            <td className="px-6 py-4 font-bold text-white max-w-[200px] truncate">{route.host}</td>
                            <td className="px-6 py-4 text-blue-300 font-mono text-sm max-w-[150px] truncate">{route.path}</td>
                            <td className="px-6 py-4">
                                <div className="flex items-center space-x-2">
                                    <StatusBadge status={route.status} />
                                    <span className="text-sm text-gray-300">{route.deployment}</span>
                                </div>
                            </td>
                            <td className="px-6 py-4 text-sm text-gray-400">
                                {route.target_service}:{route.target_port}
                            </td>
                            <td className="px-6 py-4 font-bold text-green-400">
                                {stats?.RouteStats ? (stats.RouteStats[route.id] || 0) : 0}
                            </td>
                            <td className="px-6 py-4">
                                <div className="flex flex-col space-y-1">
                                    {route.dependencies && route.dependencies.length > 0 ? (
                                        route.dependencies.map((dep) => (
                                            <div key={dep.name} className="flex items-center space-x-1.5 text-xs">
                                                <span className={`w-1.5 h-1.5 rounded-full ${route.dependency_status?.[dep.name] === 'Ready' ? 'bg-green-500' : 'bg-gray-500'}`} />
                                                <span className="text-gray-300">{dep.name}</span>
                                                {route.dependency_status && (
                                                    <StatusBadge status={route.dependency_status[dep.name] || "Unknown"} />
                                                )}
                                            </div>
                                        ))
                                    ) : (
                                        <span className="text-gray-600 text-xs italic">-</span>
                                    )}
                                </div>
                            </td>
                            <td className="px-6 py-4 text-right">
                                <div className="flex items-center justify-end space-x-2" onClick={e => e.stopPropagation()}>
                                    <Button variant="ghost" size="icon" onClick={() => onStop(route)} title="Stop Deployment">
                                        <Octagon className="w-4 h-4 text-red-400" />
                                    </Button>
                                    <Button variant="ghost" size="icon" onClick={() => onEdit(route)} title="Edit Route">
                                        <Edit2 className="w-4 h-4 text-blue-400" />
                                    </Button>
                                    <Button variant="ghost" size="icon" onClick={() => onDelete(route.id)} title="Delete Route">
                                        <Trash2 className="w-4 h-4 text-gray-400 hover:text-red-500" />
                                    </Button>
                                </div>
                            </td>
                        </tr>
                    ))}
                </tbody>
            </table>
        </div>
    );
}
