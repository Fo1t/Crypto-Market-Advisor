import { useEffect, useRef } from 'react';
import {
  ColorType,
  CrosshairMode,
  LineStyle,
  createChart,
  type IChartApi,
  type UTCTimestamp,
} from 'lightweight-charts';

import type { EquityPoint } from '../api/types';

/**
 * EquityChart draws the simulated account value over a backtest, together with
 * the drawdown measured from the running peak. The two together answer the
 * question a single "total return" number cannot: how the result was reached
 * and how deep the account went under water on the way.
 */
export function EquityChart({
  points,
  initialCapital,
  height = 260,
}: {
  points: EquityPoint[];
  initialCapital?: number;
  height?: number;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container || points.length === 0) return;

    const chart = createChart(container, {
      autoSize: true,
      layout: {
        background: { type: ColorType.Solid, color: '#0e131d' },
        textColor: '#8f9cb3',
        fontSize: 11,
      },
      grid: { vertLines: { color: '#1a2233' }, horzLines: { color: '#1a2233' } },
      crosshair: { mode: CrosshairMode.Normal },
      rightPriceScale: { borderColor: '#232c3d' },
      leftPriceScale: { visible: true, borderColor: '#232c3d' },
      timeScale: { borderColor: '#232c3d', timeVisible: true, secondsVisible: false },
      height,
    });
    chartRef.current = chart;

    const equity = chart.addAreaSeries({
      lineColor: '#4ea8de',
      topColor: 'rgba(78, 168, 222, 0.28)',
      bottomColor: 'rgba(78, 168, 222, 0.02)',
      lineWidth: 2,
      priceLineVisible: false,
      title: 'equity',
    });

    const drawdown = chart.addAreaSeries({
      lineColor: '#f2617a',
      topColor: 'rgba(242, 97, 122, 0.02)',
      bottomColor: 'rgba(242, 97, 122, 0.25)',
      lineWidth: 1,
      priceScaleId: 'left',
      priceLineVisible: false,
      lastValueVisible: false,
      title: 'drawdown %',
    });

    const equityData: { time: UTCTimestamp; value: number }[] = [];
    const drawdownData: { time: UTCTimestamp; value: number }[] = [];
    let peak = Number.NEGATIVE_INFINITY;
    let previous = -1;

    for (const point of points) {
      // lightweight-charts requires strictly increasing, unique timestamps.
      const time = Math.floor(new Date(point.t).getTime() / 1000);
      if (time <= previous) continue;
      previous = time;

      peak = Math.max(peak, point.e);
      equityData.push({ time: time as UTCTimestamp, value: point.e });
      drawdownData.push({
        time: time as UTCTimestamp,
        value: peak > 0 ? -((peak - point.e) / peak) * 100 : 0,
      });
    }
    equity.setData(equityData);
    drawdown.setData(drawdownData);

    if (initialCapital && initialCapital > 0) {
      equity.createPriceLine({
        price: initialCapital,
        color: '#8f9cb3',
        lineWidth: 1,
        lineStyle: LineStyle.Dashed,
        axisLabelVisible: true,
        title: 'start',
      });
    }
    chart.timeScale().fitContent();

    return () => {
      chart.remove();
      chartRef.current = null;
    };
  }, [points, initialCapital, height]);

  if (points.length === 0) return null;
  return <div ref={containerRef} style={{ width: '100%', height }} />;
}
