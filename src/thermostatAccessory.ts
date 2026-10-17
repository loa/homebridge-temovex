import type { CharacteristicValue, PlatformAccessory, Service } from 'homebridge';

import { configureInformation, SETPOINT_MAX_C, SETPOINT_MIN_C, SETPOINT_STEP_C } from './devices.js';
import type { TemovexPlatform } from './platform.js';
import { failureText, type ProxyClient } from './proxy.js';

/**
 * Supply-air setpoint. Current and target temperature are both the `set`
 * reading. The other air temperatures are separate sensors.
 */
export class ThermostatAccessory {
  private readonly service: Service;

  constructor(
    private readonly platform: TemovexPlatform,
    private readonly accessory: PlatformAccessory,
    private readonly client: ProxyClient,
  ) {
    configureInformation(
      this.platform.Service,
      this.platform.Characteristic,
      accessory,
      'temovex-set',
    );

    this.service = accessory.getService(this.platform.Service.Thermostat)
      || accessory.addService(this.platform.Service.Thermostat);
    this.service.setCharacteristic(this.platform.Characteristic.Name, accessory.displayName);

    const heat = this.platform.Characteristic.TargetHeatingCoolingState.HEAT;
    const heating = this.platform.Characteristic.CurrentHeatingCoolingState.HEAT;
    this.service.getCharacteristic(this.platform.Characteristic.CurrentHeatingCoolingState)
      .updateValue(heating)
      .onGet(() => heating);
    this.service.getCharacteristic(this.platform.Characteristic.TargetHeatingCoolingState)
      .setProps({ validValues: [heat] })
      .updateValue(heat)
      .onGet(() => heat)
      .onSet((value) => {
        if (value !== heat) {
          throw new this.platform.api.hap.HapStatusError(this.platform.api.hap.HAPStatus.INVALID_VALUE_IN_REQUEST);
        }
      });

    this.service.getCharacteristic(this.platform.Characteristic.CurrentTemperature)
      .setProps({
        minValue: SETPOINT_MIN_C,
        maxValue: SETPOINT_MAX_C,
        minStep: SETPOINT_STEP_C,
      })
      .onGet(() => this.getSetpoint());
    this.service.getCharacteristic(this.platform.Characteristic.TargetTemperature)
      .setProps({
        minValue: SETPOINT_MIN_C,
        maxValue: SETPOINT_MAX_C,
        minStep: SETPOINT_STEP_C,
      })
      .onGet(() => this.getSetpoint())
      .onSet((value) => this.setSetpoint(value));

    const units = this.service.getCharacteristic(this.platform.Characteristic.TemperatureDisplayUnits);
    units.setProps({
      perms: [this.platform.api.hap.Perms.PAIRED_READ, this.platform.api.hap.Perms.NOTIFY],
    });
    units.updateValue(this.platform.Characteristic.TemperatureDisplayUnits.CELSIUS);
  }

  private async getSetpoint(): Promise<CharacteristicValue> {
    try {
      const temperatures = await this.client.temperatures();
      const value = temperatures.get('set');
      if (value === undefined) {
        throw new Error('set temperature missing');
      }
      this.platform.log.debug('Supply-air setpoint:', value);
      return value;
    } catch (error) {
      this.platform.log.error('Failed to read supply-air setpoint:', failureText(error));
      throw new this.platform.api.hap.HapStatusError(this.platform.api.hap.HAPStatus.SERVICE_COMMUNICATION_FAILURE);
    }
  }

  private async setSetpoint(value: CharacteristicValue): Promise<void> {
    if (typeof value !== 'number' || !Number.isFinite(value)) {
      throw new this.platform.api.hap.HapStatusError(this.platform.api.hap.HAPStatus.INVALID_VALUE_IN_REQUEST);
    }
    try {
      await this.client.setSetpoint(value);
      this.service.updateCharacteristic(this.platform.Characteristic.CurrentTemperature, value);
      this.platform.log.info('Supply-air setpoint:', value);
    } catch (error) {
      this.platform.log.error('Failed to set supply-air setpoint:', failureText(error));
      throw new this.platform.api.hap.HapStatusError(this.platform.api.hap.HAPStatus.SERVICE_COMMUNICATION_FAILURE);
    }
  }
}
