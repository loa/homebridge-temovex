import type { CharacteristicValue, PlatformAccessory, Service } from 'homebridge';

import { configureInformation, SENSOR_MAX_C, SENSOR_MIN_C, type TemperatureName } from './devices.js';
import type { TemovexPlatform } from './platform.js';
import { failureText, type ProxyClient } from './proxy.js';

/** One measured temperature from the proxy, read when HomeKit asks. */
export class TemperatureAccessory {
  private readonly service: Service;
  private readonly name: TemperatureName;

  constructor(
    private readonly platform: TemovexPlatform,
    private readonly accessory: PlatformAccessory,
    private readonly client: ProxyClient,
  ) {
    this.name = accessory.context.device.name as TemperatureName;
    configureInformation(
      this.platform.Service,
      this.platform.Characteristic,
      accessory,
      `temovex-${this.name}`,
    );

    this.service = accessory.getService(this.platform.Service.TemperatureSensor)
      || accessory.addService(this.platform.Service.TemperatureSensor);
    this.service.setCharacteristic(this.platform.Characteristic.Name, accessory.displayName);
    this.service.getCharacteristic(this.platform.Characteristic.CurrentTemperature)
      .setProps({
        minValue: SENSOR_MIN_C,
        maxValue: SENSOR_MAX_C,
        minStep: 0.1,
      })
      .onGet(() => this.getTemperature());
  }

  private async getTemperature(): Promise<CharacteristicValue> {
    try {
      const temperatures = await this.client.temperatures();
      const value = temperatures.get(this.name);
      if (value === undefined) {
        throw new Error(`${this.name} temperature missing`);
      }
      this.platform.log.debug(this.accessory.displayName, value);
      return value;
    } catch (error) {
      this.platform.log.error(`Failed to read ${this.accessory.displayName}:`, failureText(error));
      throw new this.platform.api.hap.HapStatusError(this.platform.api.hap.HAPStatus.SERVICE_COMMUNICATION_FAILURE);
    }
  }
}
