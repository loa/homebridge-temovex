import type { Characteristic, PlatformAccessory, Service } from 'homebridge';

export const SETPOINT_MIN_C = 10;
export const SETPOINT_MAX_C = 40;
export const SETPOINT_STEP_C = 0.1;

export const SENSOR_MIN_C = -50;
export const SENSOR_MAX_C = 100;

export const temperatureDevices = [
  { name: 'set', description: 'Supply-air setpoint', kind: 'thermostat' },
  { name: 'al', description: 'Exhaust air', kind: 'sensor' },
  { name: 'fl', description: 'Extract air', kind: 'sensor' },
  { name: 'ul', description: 'Outdoor air', kind: 'sensor' },
  { name: 'tl', description: 'Supply air', kind: 'sensor' },
] as const;

export type TemperatureName = typeof temperatureDevices[number]['name'];
export type TemperatureDevice = typeof temperatureDevices[number];

const temperatureNames = new Set<string>(temperatureDevices.map((device) => device.name));

export function isTemperatureName(name: string): name is TemperatureName {
  return temperatureNames.has(name);
}

export function configureInformation(
  service: typeof Service,
  characteristic: typeof Characteristic,
  accessory: PlatformAccessory,
  serial: string,
): void {
  const information = accessory.getService(service.AccessoryInformation);
  if (!information) {
    return;
  }
  information
    .setCharacteristic(characteristic.Manufacturer, 'REC')
    .setCharacteristic(characteristic.Model, 'Temovex')
    .setCharacteristic(characteristic.SerialNumber, serial);
}
