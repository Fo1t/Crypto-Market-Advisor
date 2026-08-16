import { useEffect, useRef } from 'react';
import {
  ColorType,
  CrosshairMode,
  LineStyle,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type SeriesMarker,
  type UTCTimestamp,
} from 'lightweight-charts';

import type { Candle, Level } from '../api/types';

interface Overlay {
  label: string;
  color: string;
  points: { time: UTCTimestamp; value: number }[];
}

export interface NewsMarker {
  time: string;
  title: string;
  critical: boolean;
}

/** ChartMarker is a generic annotation, used for backtest fills. */
export interface ChartMarker {
  time: string;
  text: string;
  position: 'aboveBar' | 'belowBar';
  color: string;
  shape: 'arrowUp' | 'arrowDown' | 'circle' | 'square';
}

/**
 * CandleChart renders price, volume, moving-average overlays and horizontal
 * support/resistance levels. Deliberately few overlays are drawn at once: a
 * readable chart beats a complete one.
 */
export function CandleChart({
  candles,
  levels = [],
  overlays = [],
  newsMarkers = [],
  markers = [],
  height = 420,
}: {
  candles: Candle[];
  levels?: Level[];
  overlays?: Overlay[];
  newsMarkers?: NewsMarker[];
  markers?: ChartMarker[];
  height?: number;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const candleSeriesRef = useRef<ISeriesApi<'Candlestick'> | null>(null);
  const volumeSeriesRef = useRef<ISeriesApi<'Histogram'> | null>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const chart = createChart(container, {
      autoSize: true,
      layout: {
        background: { type: ColorType.Solid, color: '#0e131d' },
        textColor: '#8f9cb3',
        fontSize: 11,
      },
      grid: {
        vertLines: { color: '#1a2233' },
        horzLines: { color: '#1a2233' },
      },
      crosshair: { mode: CrosshairMode.Normal },
      rightPriceScale: { borderColor: '#232c3d' },
      timeScale: { borderColor: '#232c3d', timeVisible: true, secondsVisible: false },
    });

    const candleSeries = chart.addCandlestickSeries({
      upColor: '#35c98a',
      downColor: '#f2617a',
      borderUpColor: '#35c98a',
      borderDownColor: '#f2617a',
      wickUpColor: '#35c98a',
      wickDownColor: '#f2617a',
    });

    const volumeSeries = chart.addHistogramSeries({
      priceFormat: { type: 'volume' },
      priceScaleId: 'volume',
      color: '#2c3a4f',
    });
    chart.priceScale('volume').applyOptions({
      scaleMargins: { top: 0.85, bottom: 0 },
    });

    chartRef.current = chart;
    candleSeriesRef.current = candleSeries;
    volumeSeriesRef.current = volumeSeries;

    return () => {
      chart.remove();
      chartRef.current = null;
      candleSeriesRef.current = null;
      volumeSeriesRef.current = null;
    };
  }, []);

  useEffect(() => {
    const candleSeries = candleSeriesRef.current;
    const volumeSeries = volumeSeriesRef.current;
    const chart = chartRef.current;
    if (!candleSeries || !volumeSeries || !chart) return;

    const bars = candles
      .map((c) => ({
        time: (new Date(c.open_time).getTime() / 1000) as UTCTimestamp,
        open: c.open,
        high: c.high,
        low: c.low,
        close: c.close,
      }))
      .sort((a, b) => a.time - b.time);

    candleSeries.setData(bars);

    // Volume is only meaningful when the data source actually provides it.
    const hasVolume = candles.some((c) => c.volume > 0);
    volumeSeries.setData(
      hasVolume
        ? candles
            .map((c) => ({
              time: (new Date(c.open_time).getTime() / 1000) as UTCTimestamp,
              value: c.volume,
              color: c.close >= c.open ? 'rgba(53,201,138,0.35)' : 'rgba(242,97,122,0.35)',
            }))
            .sort((a, b) => a.time - b.time)
        : [],
    );

    chart.timeScale().fitContent();
  }, [candles]);

  useEffect(() => {
    const candleSeries = candleSeriesRef.current;
    if (!candleSeries) return;
    const news: SeriesMarker<UTCTimestamp>[] = newsMarkers.map((marker) => ({
      time: (new Date(marker.time).getTime() / 1000) as UTCTimestamp,
      position: 'aboveBar' as const,
      color: marker.critical ? '#f2617a' : '#4ea8de',
      shape: marker.critical ? ('square' as const) : ('circle' as const),
      text: marker.critical ? `! ${marker.title}` : marker.title,
    }));
    const custom: SeriesMarker<UTCTimestamp>[] = markers.map((marker) => ({
      time: (new Date(marker.time).getTime() / 1000) as UTCTimestamp,
      position: marker.position,
      color: marker.color,
      shape: marker.shape,
      text: marker.text,
    }));
    candleSeries.setMarkers([...news, ...custom].sort((a, b) => a.time - b.time));
    return () => candleSeries.setMarkers([]);
  }, [newsMarkers, markers]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;

    const series = overlays.map((overlay) => {
      const line = chart.addLineSeries({
        color: overlay.color,
        lineWidth: 1,
        priceLineVisible: false,
        lastValueVisible: false,
        title: overlay.label,
      });
      line.setData(overlay.points.slice().sort((a, b) => a.time - b.time));
      return line;
    });

    return () => {
      if (chartRef.current !== chart) return;
      series.forEach((line) => chart.removeSeries(line));
    };
  }, [overlays]);

  useEffect(() => {
    const candleSeries = candleSeriesRef.current;
    if (!candleSeries) return;

    const lines = levels.slice(0, 8).map((level) =>
      candleSeries.createPriceLine({
        price: level.price,
        color: level.type === 'resistance' ? 'rgba(242,97,122,0.55)' : 'rgba(53,201,138,0.55)',
        lineWidth: 1,
        lineStyle: LineStyle.Dashed,
        axisLabelVisible: true,
        title: `${level.type === 'resistance' ? 'R' : 'S'} ${level.touches}`,
      }),
    );

    return () => {
      if (candleSeriesRef.current !== candleSeries) return;
      lines.forEach((line) => candleSeries.removePriceLine(line));
    };
  }, [levels]);

  return <div className="chart" ref={containerRef} style={{ height }} />;
}
